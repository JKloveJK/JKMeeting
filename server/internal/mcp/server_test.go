package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeService struct{}

func (fakeService) ListRecordings(context.Context, int) (any, error) {
	return map[string]any{"recordings": []any{}}, nil
}
func (fakeService) GetTranscript(context.Context, string) (any, error) {
	return map[string]any{"status": "done", "transcript": "你好"}, nil
}
func (fakeService) GetInsights(context.Context, string) (any, error) {
	return map[string]any{"status": "none"}, nil
}
func (fakeService) SearchMeetings(context.Context, string, int) (any, error) {
	return map[string]any{"hits": []any{}}, nil
}
func (fakeService) AskMeetings(context.Context, string, int) (any, error) {
	return map[string]any{"answer": "资料不足", "insufficient": true}, nil
}

func runSession(t *testing.T, input string) []map[string]any {
	t.Helper()
	server := NewServer(fakeService{}, nil)
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve 失败：%v", err)
	}
	var responses []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("响应不是合法 JSON：%v", err)
		}
		responses = append(responses, row)
	}
	return responses
}

func TestInitializeToolsListAndCall(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_recordings","arguments":{"limit":5}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
	}, "\n") + "\n"

	responses := runSession(t, input)
	if len(responses) != 4 {
		t.Fatalf("期望 4 条响应（通知不回），实际 %d：%v", len(responses), responses)
	}
	initResult, ok := responses[0]["result"].(map[string]any)
	if !ok || initResult["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize 响应异常：%v", responses[0])
	}
	listResult := responses[1]["result"].(map[string]any)
	tools, ok := listResult["tools"].([]any)
	if !ok || len(tools) != 5 {
		t.Fatalf("tools/list 期望 5 个工具，实际：%v", listResult)
	}
	callResult := responses[2]["result"].(map[string]any)
	if callResult["isError"] == true {
		t.Fatalf("list_recordings 调用不应报错：%v", callResult)
	}
	if responses[3]["error"] == nil {
		t.Fatalf("未知工具应返回协议错误：%v", responses[3])
	}
}

func TestMissingRequiredArgIsToolError(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_transcript","arguments":{}}}` + "\n"
	responses := runSession(t, input)
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("缺少必填参数应为工具级错误：%v", result)
	}
}
