package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func reply(content string, prompt, completion int) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		"usage":   map[string]int{"prompt_tokens": prompt, "completion_tokens": completion},
	})
	return string(body)
}

func newClient(url string) *Client {
	return NewClient(Config{BaseURL: url, Model: "fixture", Timeout: time.Second, MaxRetries: 2, Concurrency: 1, Backoff: time.Millisecond})
}

var schema = json.RawMessage(`{"type":"object"}`)

func requireOK(raw []byte) error {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value["ok"] != true {
		return errors.New("ok 字段缺失")
	}
	return nil
}

func TestChatJSONSendsSchemaAndReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", request.URL.Path)
		}
		var body struct {
			Model          string `json:"model"`
			Messages       []Message
			Temperature    float64 `json:"temperature"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body.ResponseFormat.Type != "json_schema" || string(body.ResponseFormat.JSONSchema.Schema) != `{"type":"object"}` ||
			len(body.Messages) != 2 || body.Model != "fixture" {
			t.Errorf("request = %+v", body)
		}
		fmt.Fprint(writer, reply(`{"ok":true}`, 120, 30))
	}))
	defer server.Close()

	result, err := newClient(server.URL).ChatJSON(context.Background(),
		[]Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, schema, requireOK)
	if err != nil {
		t.Fatalf("ChatJSON() error = %v", err)
	}
	if string(result.Content) != `{"ok":true}` || result.Usage != (Usage{PromptTokens: 120, CompletionTokens: 30}) ||
		result.Attempts != 1 || result.FormatFailures != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestChatJSONRetriesTransientAndFormatErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(writer, "busy", http.StatusServiceUnavailable)
		case 2:
			fmt.Fprint(writer, reply(`{"ok":false}`, 100, 10))
		default:
			fmt.Fprint(writer, reply(`{"ok":true}`, 100, 20))
		}
	}))
	defer server.Close()

	result, err := newClient(server.URL).ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK)
	if err != nil {
		t.Fatalf("ChatJSON() error = %v", err)
	}
	// 失败尝试消耗的 token 同样计入成本。
	if result.Attempts != 3 || result.FormatFailures != 1 || result.Usage.PromptTokens != 200 || result.Usage.CompletionTokens != 30 {
		t.Fatalf("result = %+v", result)
	}
}

func TestChatJSONDoesNotRetryClientErrorsAndStopsAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	badRequest := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(writer, "context too long", http.StatusBadRequest)
	}))
	defer badRequest.Close()
	_, err := newClient(badRequest.URL).ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK)
	var status *StatusError
	if !errors.As(err, &status) || status.Code != http.StatusBadRequest || calls.Load() != 1 || IsRetryable(err) {
		t.Fatalf("err = %v calls = %d", err, calls.Load())
	}

	calls.Store(0)
	alwaysBroken := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(writer, reply(`not json`, 10, 5))
	}))
	defer alwaysBroken.Close()
	result, err := newClient(alwaysBroken.URL).ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK)
	if !errors.Is(err, ErrInvalidFormat) || calls.Load() != 3 || result.FormatFailures != 3 || !IsRetryable(err) {
		t.Fatalf("err = %v calls = %d result = %+v", err, calls.Load(), result)
	}
}

func TestChatJSONSendsMaxTokensAndDoesNotRetryTruncation(t *testing.T) {
	var calls atomic.Int32
	var gotMaxTokens int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		gotMaxTokens = body.MaxTokens
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": `{"answer":"重复重复`}, "finish_reason": "length"}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 64},
		})
		writer.Write(payload)
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Timeout: time.Second, MaxRetries: 2, Backoff: time.Millisecond, MaxTokens: 64})

	result, err := client.ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK)
	if !errors.Is(err, ErrTruncated) || IsRetryable(err) || calls.Load() != 1 || gotMaxTokens != 64 ||
		result.FormatFailures != 1 || result.Usage.CompletionTokens != 64 {
		t.Fatalf("err = %v calls = %d max_tokens = %d result = %+v", err, calls.Load(), gotMaxTokens, result)
	}
}

func TestChatJSONTimesOutSlowServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond, MaxRetries: 1, Concurrency: 1, Backoff: time.Millisecond})
	started := time.Now()
	result, err := client.ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK)
	if err == nil || !IsRetryable(err) || result.Attempts != 2 || time.Since(started) > time.Second {
		t.Fatalf("err = %v result = %+v elapsed = %v", err, result, time.Since(started))
	}
}

func TestChatJSONRespectsConcurrencyLimit(t *testing.T) {
	var inFlight, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := inFlight.Add(1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(writer, reply(`{"ok":true}`, 1, 1))
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Timeout: time.Second, Concurrency: 2, Backoff: time.Millisecond})

	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := client.ChatJSON(context.Background(), []Message{{Role: "user", Content: "u"}}, schema, requireOK); err != nil {
				t.Errorf("ChatJSON() error = %v", err)
			}
		}()
	}
	group.Wait()
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
}
