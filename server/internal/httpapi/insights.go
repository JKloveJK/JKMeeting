package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/insights"
	"github.com/jiankailei/jkmeeting/server/internal/observability"
)

type insightsStore interface {
	FindInsights(context.Context, uuid.UUID) (insights.Record, error)
}

type insightsResponse struct {
	RecordingID      string          `json:"recording_id"`
	Status           string          `json:"status"`
	Result           json.RawMessage `json:"result"`
	Model            *string         `json:"model"`
	PromptTokens     *int            `json:"prompt_tokens"`
	CompletionTokens *int            `json:"completion_tokens"`
	LLMCalls         *int            `json:"llm_calls"`
	AnalysisMS       *int            `json:"analysis_ms"`
	ErrorMessage     *string         `json:"error_message"`
}

func (api *RecordingAPI) WithInsights(store insightsStore) *RecordingAPI {
	api.insights = store
	return api
}

// getInsights 对转写尚未完成或 P3 之前的录音返回 status=none，而不是 404，便于客户端统一轮询。
func (api *RecordingAPI) getInsights(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "/insights")
	if !ok {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	if _, err := api.store.FindByID(request.Context(), id); err != nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "录音不存在")
		return
	}
	body := insightsResponse{RecordingID: id.String(), Status: "none", Result: json.RawMessage("null")}
	if api.insights != nil {
		record, err := api.insights.FindInsights(request.Context(), id)
		switch {
		case errors.Is(err, insights.ErrNotFound):
		case err != nil:
			api.writeError(response, tc.RequestID, http.StatusInternalServerError, "查询会议纪要失败")
			return
		default:
			body.Status = string(record.Status)
			if record.Status == insights.StatusReady && len(record.Result) > 0 {
				body.Result = record.Result
			}
			body.Model, body.PromptTokens, body.CompletionTokens = record.Model, record.PromptTokens, record.CompletionTokens
			body.LLMCalls, body.AnalysisMS, body.ErrorMessage = record.LLMCalls, record.AnalysisMS, record.ErrorMessage
		}
	}
	api.writeJSON(response, http.StatusOK, body)
}
