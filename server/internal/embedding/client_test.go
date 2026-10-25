package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func embeddingServer(t *testing.T, handler func(call int32, inputs []string) (int, any)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		status, payload := handler(atomic.AddInt32(&calls, 1), body.Input)
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func data(vectors ...[]float32) map[string]any {
	items := make([]map[string]any, len(vectors))
	// 倒序返回，验证按 index 还原顺序。
	for i := range vectors {
		j := len(vectors) - 1 - i
		items[i] = map[string]any{"index": j, "embedding": vectors[j]}
	}
	return map[string]any{"data": items, "usage": map[string]int{"prompt_tokens": 7}}
}

func TestEmbedNormalizesAndKeepsOrderAfterRetry(t *testing.T) {
	server, calls := embeddingServer(t, func(call int32, inputs []string) (int, any) {
		if call == 1 {
			return http.StatusServiceUnavailable, map[string]string{"error": "loading"}
		}
		return http.StatusOK, data([]float32{3, 4}, []float32{0, 2})
	})
	client := NewClient(Config{BaseURL: server.URL, Dim: 2, MaxRetries: 1, Backoff: time.Millisecond})

	vectors, tokens, err := client.Embed(context.Background(), []string{"a", "b"})
	if err != nil || *calls != 2 || tokens != 7 {
		t.Fatalf("err = %v calls = %d tokens = %d", err, *calls, tokens)
	}
	if math.Abs(float64(vectors[0][0])-0.6) > 1e-6 || math.Abs(float64(vectors[0][1])-0.8) > 1e-6 || vectors[1][1] != 1 {
		t.Fatalf("vectors = %v", vectors)
	}
}

func TestEmbedDimensionMismatchIsNotRetried(t *testing.T) {
	server, calls := embeddingServer(t, func(int32, []string) (int, any) {
		return http.StatusOK, data([]float32{1, 2, 3})
	})
	client := NewClient(Config{BaseURL: server.URL, Dim: 1024, MaxRetries: 3, Backoff: time.Millisecond})
	if _, _, err := client.Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrDimension) || *calls != 1 {
		t.Fatalf("err = %v calls = %d", err, *calls)
	}
	if IsRetryable(ErrDimension) {
		t.Fatal("维度错误不可重试")
	}
}
