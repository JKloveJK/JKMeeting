// Package embedding 封装 OpenAI 兼容的 /v1/embeddings 接口（本机 llama-server 加载 bge-m3）。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jiankailei/jkmeeting/server/internal/llm"
)

// ErrDimension 表示向量维度与配置不符，属于配置错误，重试无效。
var ErrDimension = errors.New("embedding 维度与配置不一致")

func IsRetryable(err error) bool {
	return !errors.Is(err, ErrDimension) && llm.IsRetryable(err)
}

type Config struct {
	BaseURL    string
	Model      string
	Dim        int
	Timeout    time.Duration
	MaxRetries int
	Backoff    time.Duration
}

type Client struct {
	config Config
	http   *http.Client
}

func NewClient(config Config) *Client {
	if config.Backoff <= 0 {
		config.Backoff = 300 * time.Millisecond
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &Client{config: config, http: &http.Client{}}
}

func (client *Client) Dim() int { return client.config.Dim }

// Embed 返回与 inputs 一一对应、L2 归一化后的向量；失败按 IsRetryable 规则重试。
func (client *Client) Embed(ctx context.Context, inputs []string) ([][]float32, int, error) {
	if len(inputs) == 0 {
		return nil, 0, nil
	}
	var lastErr error
	for attempt := 0; attempt <= client.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(client.config.Backoff << (attempt - 1)):
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		vectors, tokens, err := client.embedOnce(ctx, inputs)
		if err == nil {
			return vectors, tokens, nil
		}
		lastErr = err
		if !IsRetryable(err) || ctx.Err() != nil {
			break
		}
	}
	return nil, 0, lastErr
}

func (client *Client) embedOnce(ctx context.Context, inputs []string) ([][]float32, int, error) {
	payload, err := json.Marshal(map[string]any{"model": client.config.Model, "input": inputs})
	if err != nil {
		return nil, 0, err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, client.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, client.config.BaseURL+"/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, 0, err
	}
	if response.StatusCode != http.StatusOK {
		text := strings.TrimSpace(string(body))
		if runes := []rune(text); len(runes) > 200 {
			text = string(runes[:200])
		}
		return nil, 0, &llm.StatusError{Code: response.StatusCode, Body: text}
	}
	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Data) != len(inputs) {
		return nil, 0, fmt.Errorf("embedding 响应结构无效：期望 %d 条", len(inputs))
	}
	vectors := make([][]float32, len(inputs))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(inputs) {
			return nil, 0, fmt.Errorf("embedding 响应序号越界：%d", item.Index)
		}
		if len(item.Embedding) != client.config.Dim {
			return nil, 0, fmt.Errorf("%w：期望 %d，实际 %d", ErrDimension, client.config.Dim, len(item.Embedding))
		}
		vectors[item.Index] = normalize(item.Embedding)
	}
	return vectors, decoded.Usage.PromptTokens, nil
}

func normalize(vector []float32) []float32 {
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return vector
	}
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = float32(float64(value) / norm)
	}
	return out
}
