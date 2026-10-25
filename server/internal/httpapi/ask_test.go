package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
)

type fakeAsker struct {
	answer rag.Answer
	err    error
	gotK   int
}

func (asker *fakeAsker) Ask(_ context.Context, _ string, k int) (rag.Answer, error) {
	asker.gotK = k
	return asker.answer, asker.err
}

func postAsk(t *testing.T, asker *fakeAsker, body string) (int, map[string]any) {
	t.Helper()
	api := NewRecordingAPI(&fakeRecordingStore{}, &fakeStorage{}, &fakeDispatcher{}, "fixture-token").WithAsk(asker, 5)
	request := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.Code, decoded
}

func TestAskReturnsAnswerWithCitations(t *testing.T) {
	recordingID := uuid.New()
	asker := &fakeAsker{answer: rag.Answer{
		Text:      "下周三发布",
		Citations: []rag.Citation{{Number: 1, Hit: rag.Hit{RecordingID: recordingID, ChunkIndex: 0, Content: "下周三发布 v2.1", Distance: 0.21}}},
		Retrieved: make([]rag.Hit, 5),
	}}
	code, body := postAsk(t, asker, `{"question":"什么时候发布？"}`)
	if code != http.StatusOK || body["answer"] != "下周三发布" || body["insufficient"] != false || asker.gotK != 5 {
		t.Fatalf("code = %d body = %v k = %d", code, body, asker.gotK)
	}
	citations := body["citations"].([]any)
	if len(citations) != 1 || citations[0].(map[string]any)["recording_id"] != recordingID.String() {
		t.Fatalf("citations = %v", citations)
	}
}

func TestAskValidatesInputAndMapsErrors(t *testing.T) {
	for name, body := range map[string]string{
		"empty":    `{"question":"  "}`,
		"too long": fmt.Sprintf(`{"question":%q}`, strings.Repeat("问", 501)),
		"bad k":    `{"question":"q","top_k":11}`,
		"bad json": `{`,
	} {
		if code, _ := postAsk(t, &fakeAsker{}, body); code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d", name, code)
		}
	}
	if code, _ := postAsk(t, &fakeAsker{err: rag.ErrEmbedding}, `{"question":"q"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("embedding error code = %d", code)
	}
	if code, _ := postAsk(t, &fakeAsker{err: fmt.Errorf("llm: %w", context.DeadlineExceeded)}, `{"question":"q"}`); code != http.StatusGatewayTimeout {
		t.Fatalf("timeout code = %d", code)
	}
	if code, _ := postAsk(t, &fakeAsker{err: errors.New("x")}, `{"question":"q","top_k":3}`); code != http.StatusServiceUnavailable {
		t.Fatalf("generic error code = %d", code)
	}
	if route := classifyRoute(http.MethodPost, "/v1/ask"); route != "POST /v1/ask" {
		t.Fatalf("route = %q", route)
	}
}
