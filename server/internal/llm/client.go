// Package llm 封装 OpenAI 兼容的 chat completions 接口（本机 llama-server），
// 负责 JSON Schema 约束输出、重试分类、并发上限和 token 统计，不包含业务语义。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidFormat 表示模型输出无法解析或未通过调用方校验；可重试。
var ErrInvalidFormat = errors.New("模型输出格式无效")

// ErrTruncated 表示输出达到 max_tokens 被截断。温度为 0 时同一输入重试会得到同样结果，因此不重试。
var ErrTruncated = errors.New("模型输出超过 max_tokens 被截断")

type StatusError struct {
	Code int
	Body string
}

func (err *StatusError) Error() string {
	return fmt.Sprintf("LLM 返回 HTTP %d：%s", err.Code, err.Body)
}

// IsRetryable 区分值得重试的错误：网络与超时、5xx、429、格式错误；其余 4xx 重试无效。
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, ErrTruncated) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.Code >= 500 || status.Code == http.StatusTooManyRequests
	}
	return !errors.Is(err, context.Canceled)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (usage Usage) Add(other Usage) Usage {
	return Usage{PromptTokens: usage.PromptTokens + other.PromptTokens, CompletionTokens: usage.CompletionTokens + other.CompletionTokens}
}

// Result 汇总一次逻辑调用（含重试）的结果；Usage 包含失败尝试消耗的 token。
type Result struct {
	Content        json.RawMessage
	Usage          Usage
	Attempts       int
	FormatFailures int
	Elapsed        time.Duration
}

type Config struct {
	BaseURL     string
	Model       string
	Timeout     time.Duration
	MaxRetries  int
	Concurrency int
	Backoff     time.Duration
	Temperature float64
	// MaxTokens 限制单次输出长度，防止约束解码陷入重复时一直生成到超时；0 表示 1024。
	MaxTokens int
}

type Client struct {
	config Config
	http   *http.Client
	slots  chan struct{}
}

func NewClient(config Config) *Client {
	if config.Concurrency <= 0 {
		config.Concurrency = 1
	}
	if config.Backoff <= 0 {
		config.Backoff = 500 * time.Millisecond
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 1024
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &Client{config: config, http: &http.Client{}, slots: make(chan struct{}, config.Concurrency)}
}

func (client *Client) Model() string { return client.config.Model }

// ChatJSON 请求按 schema 约束解码的 JSON 输出，并用 validate 做语义校验；失败按 IsRetryable 规则重试。
func (client *Client) ChatJSON(ctx context.Context, messages []Message, schema json.RawMessage, validate func([]byte) error) (Result, error) {
	select {
	case client.slots <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-client.slots }()

	started := time.Now()
	var result Result
	var lastErr error
	for attempt := 0; attempt <= client.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(client.config.Backoff << (attempt - 1)):
			case <-ctx.Done():
				result.Elapsed = time.Since(started)
				return result, ctx.Err()
			}
		}
		result.Attempts++
		content, usage, err := client.complete(ctx, messages, schema)
		result.Usage = result.Usage.Add(usage)
		if errors.Is(err, ErrTruncated) {
			result.FormatFailures++
		}
		if err == nil {
			if validateErr := validate(content); validateErr != nil {
				result.FormatFailures++
				err = fmt.Errorf("%w：%v", ErrInvalidFormat, validateErr)
			} else {
				result.Content = content
				result.Elapsed = time.Since(started)
				return result, nil
			}
		}
		lastErr = err
		if !IsRetryable(err) || ctx.Err() != nil {
			break
		}
	}
	result.Elapsed = time.Since(started)
	return result, lastErr
}

func (client *Client) complete(ctx context.Context, messages []Message, schema json.RawMessage) (json.RawMessage, Usage, error) {
	payload, err := json.Marshal(map[string]any{
		"model":       client.config.Model,
		"messages":    messages,
		"temperature": client.config.Temperature,
		"max_tokens":  client.config.MaxTokens,
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "result", "strict": true, "schema": schema},
		},
	})
	if err != nil {
		return nil, Usage{}, err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, client.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, client.config.BaseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, Usage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, Usage{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, Usage{}, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, Usage{}, &StatusError{Code: response.StatusCode, Body: truncate(string(body), 200)}
	}
	var decoded struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Choices) == 0 {
		return nil, decoded.Usage, fmt.Errorf("%w：响应结构无效", ErrInvalidFormat)
	}
	if decoded.Choices[0].FinishReason == "length" {
		return nil, decoded.Usage, fmt.Errorf("%w（%d token），输出开头：%s", ErrTruncated,
			decoded.Usage.CompletionTokens, truncate(decoded.Choices[0].Message.Content, 120))
	}
	return json.RawMessage(decoded.Choices[0].Message.Content), decoded.Usage, nil
}

func truncate(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
