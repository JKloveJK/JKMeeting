package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type fakeInsights struct {
	record *insights.Record
	err    error
}

func (store fakeInsights) FindInsights(context.Context, uuid.UUID) (insights.Record, error) {
	if store.err != nil {
		return insights.Record{}, store.err
	}
	if store.record == nil {
		return insights.Record{}, insights.ErrNotFound
	}
	return *store.record, nil
}

func getInsights(t *testing.T, store fakeInsights) (int, map[string]any) {
	t.Helper()
	id := uuid.New()
	api := NewRecordingAPI(&fakeRecordingStore{recording: recording.Recording{ID: id}}, &fakeStorage{}, &fakeDispatcher{}, "fixture-token").
		WithInsights(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String()+"/insights", nil)
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.Code, body
}

func TestGetInsightsReportsStatusAndResult(t *testing.T) {
	model, tokens, failure := "qwen", 321, "纪要抽取失败：LLM 调用超时"
	result := json.RawMessage(`{"summary":"讨论发布","decisions":[],"action_items":[],"viewpoints":[]}`)
	cases := map[string]struct {
		store      fakeInsights
		wantStatus string
		wantResult bool
	}{
		"not created": {fakeInsights{}, "none", false},
		"pending":     {fakeInsights{record: &insights.Record{Status: insights.StatusPending}}, "pending", false},
		"ready":       {fakeInsights{record: &insights.Record{Status: insights.StatusReady, Result: result, Model: &model, PromptTokens: &tokens}}, "ready", true},
		"failed":      {fakeInsights{record: &insights.Record{Status: insights.StatusFailed, ErrorMessage: &failure}}, "failed", false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			code, body := getInsights(t, testCase.store)
			if code != http.StatusOK || body["status"] != testCase.wantStatus {
				t.Fatalf("code = %d body = %v", code, body)
			}
			if (body["result"] != nil) != testCase.wantResult {
				t.Fatalf("result = %v", body["result"])
			}
			if testCase.wantResult && (body["model"] != "qwen" || body["prompt_tokens"] != float64(321)) {
				t.Fatalf("usage fields = %v", body)
			}
			if testCase.wantStatus == "failed" && body["error_message"] != failure {
				t.Fatalf("error_message = %v", body["error_message"])
			}
		})
	}

	if code, _ := getInsights(t, fakeInsights{err: errors.New("db down")}); code != http.StatusInternalServerError {
		t.Fatalf("store error code = %d", code)
	}
	if route := classifyRoute(http.MethodGet, "/v1/recordings/abc/insights"); route != "GET /v1/recordings/{id}/insights" {
		t.Fatalf("route = %q", route)
	}
}
