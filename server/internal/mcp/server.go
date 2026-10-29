// Package mcp 实现最小 MCP（Model Context Protocol）stdio 服务端：
// newline-delimited JSON-RPC 2.0，支持 initialize / ping / tools/list / tools/call。
// stdout 只写协议消息，人类可读日志一律走 stderr（由调用方注入 logger）。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// Service 是工具集对业务能力的最小依赖面，便于用 fake 做协议单测。
type Service interface {
	ListRecordings(ctx context.Context, limit int) (any, error)
	GetTranscript(ctx context.Context, recordingID string) (any, error)
	GetInsights(ctx context.Context, recordingID string) (any, error)
	SearchMeetings(ctx context.Context, query string, topK int) (any, error)
	AskMeetings(ctx context.Context, question string, topK int) (any, error)
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	call        func(ctx context.Context, args map[string]any) (any, error)
}

type Server struct {
	service Service
	tools   []tool
	logger  *slog.Logger
}

func NewServer(service Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{service: service, logger: logger}
	server.tools = []tool{
		{
			Name:        "list_recordings",
			Description: "列出最近的录音（id、状态、时长、创建时间），用于先定位会议再查纪要或转写。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":50,"default":10}}}`),
			call: func(ctx context.Context, args map[string]any) (any, error) {
				return service.ListRecordings(ctx, intArg(args, "limit", 10))
			},
		},
		{
			Name:        "get_transcript",
			Description: "按录音 id 获取转写状态与全文。",
			InputSchema: json.RawMessage(`{"type":"object","required":["recording_id"],"properties":{"recording_id":{"type":"string","format":"uuid"}}}`),
			call: func(ctx context.Context, args map[string]any) (any, error) {
				id, err := stringArg(args, "recording_id")
				if err != nil {
					return nil, err
				}
				return service.GetTranscript(ctx, id)
			},
		},
		{
			Name:        "get_insights",
			Description: "按录音 id 获取结构化纪要状态与结果（摘要、决议、待办、参会人观点）。",
			InputSchema: json.RawMessage(`{"type":"object","required":["recording_id"],"properties":{"recording_id":{"type":"string","format":"uuid"}}}`),
			call: func(ctx context.Context, args map[string]any) (any, error) {
				id, err := stringArg(args, "recording_id")
				if err != nil {
					return nil, err
				}
				return service.GetInsights(ctx, id)
			},
		},
		{
			Name:        "search_meetings",
			Description: "跨会议语义检索：返回与 query 最相关的转写片段（含录音 id 与距离），不调用 LLM 生成答案。",
			InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","minLength":1},"top_k":{"type":"integer","minimum":1,"maximum":10,"default":5}}}`),
			call: func(ctx context.Context, args map[string]any) (any, error) {
				query, err := stringArg(args, "query")
				if err != nil {
					return nil, err
				}
				return service.SearchMeetings(ctx, query, intArg(args, "top_k", 5))
			},
		},
		{
			Name:        "ask_meetings",
			Description: "跨会议问答：基于检索片段由 LLM 作答并给出引用；资料不足时明确拒答，不编造。",
			InputSchema: json.RawMessage(`{"type":"object","required":["question"],"properties":{"question":{"type":"string","minLength":1},"top_k":{"type":"integer","minimum":1,"maximum":10,"default":5}}}`),
			call: func(ctx context.Context, args map[string]any) (any, error) {
				question, err := stringArg(args, "question")
				if err != nil {
					return nil, err
				}
				return service.AskMeetings(ctx, question, intArg(args, "top_k", 5))
			},
		},
	}
	return server
}

// Serve 逐行读取 JSON-RPC 请求直到 EOF；通知（无 id）不回响应。
func (server *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(writer)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		response := server.handle(ctx, line)
		if response == nil {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (server *Server) handle(ctx context.Context, line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return &response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}
	}
	if len(req.ID) == 0 { // notification
		server.logger.Debug("mcp notification", "method", req.Method)
		return nil
	}
	reply := func(result any) *response { return &response{JSONRPC: "2.0", ID: req.ID, Result: result} }
	fail := func(code int, message string) *response {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: message}}
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = "2024-11-05"
		}
		return reply(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "jkmeeting-mcp", "version": "0.1.0"},
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		type toolInfo struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		infos := make([]toolInfo, 0, len(server.tools))
		for _, item := range server.tools {
			infos = append(infos, toolInfo{item.Name, item.Description, item.InputSchema})
		}
		return reply(map[string]any{"tools": infos})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return fail(-32602, "invalid params")
		}
		for _, item := range server.tools {
			if item.Name != params.Name {
				continue
			}
			result, err := item.call(ctx, params.Arguments)
			if err != nil {
				server.logger.Warn("mcp tool failed", "tool", params.Name, "error", err)
				return reply(toolResult(errorText(err), true))
			}
			payload, err := json.Marshal(result)
			if err != nil {
				return reply(toolResult(fmt.Sprintf("结果序列化失败：%v", err), true))
			}
			return reply(toolResult(string(payload), false))
		}
		return fail(-32602, "unknown tool: "+params.Name)
	default:
		return fail(-32601, "method not found: "+req.Method)
	}
}

// toolResult 按 MCP 约定包装为 text content；isError 表示工具级失败（非协议错误）。
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func errorText(err error) string { return err.Error() }

func stringArg(args map[string]any, name string) (string, error) {
	value, ok := args[name].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("缺少必填参数：%s", name)
	}
	return value, nil
}

func intArg(args map[string]any, name string, fallback int) int {
	if value, ok := args[name].(float64); ok && value > 0 {
		return int(value)
	}
	return fallback
}
