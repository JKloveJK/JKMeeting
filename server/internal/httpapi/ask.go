package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jiankailei/jkmeeting/server/internal/observability"
	"github.com/jiankailei/jkmeeting/server/internal/rag"
)

const (
	maxQuestionRunes = 500
	maxTopK          = 10
)

type asker interface {
	Ask(context.Context, string, int) (rag.Answer, error)
}

type askRequest struct {
	Question string `json:"question"`
	TopK     int    `json:"top_k"`
}

type askCitation struct {
	Number      int     `json:"number"`
	RecordingID string  `json:"recording_id"`
	ChunkIndex  int     `json:"chunk_index"`
	Content     string  `json:"content"`
	Distance    float64 `json:"distance"`
}

type askResponse struct {
	Answer           string        `json:"answer"`
	Insufficient     bool          `json:"insufficient"`
	Citations        []askCitation `json:"citations"`
	Retrieved        int           `json:"retrieved"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	LatencyMS        int64         `json:"latency_ms"`
	RequestID        string        `json:"request_id"`
}

func (api *RecordingAPI) WithAsk(asker asker, defaultTopK int) *RecordingAPI {
	api.asker = asker
	api.defaultTopK = defaultTopK
	return api
}

func (api *RecordingAPI) ask(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	if api.asker == nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "接口不存在")
		return
	}
	var body askRequest
	if err := json.NewDecoder(io.LimitReader(request.Body, 16<<10)).Decode(&body); err != nil {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "请求体无效")
		return
	}
	question := strings.TrimSpace(body.Question)
	if question == "" || utf8.RuneCountInString(question) > maxQuestionRunes {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "问题长度须为 1～500 字")
		return
	}
	topK := body.TopK
	if topK == 0 {
		topK = api.defaultTopK
	}
	if topK < 1 || topK > maxTopK {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "top_k 须为 1～10")
		return
	}

	answer, err := api.asker.Ask(request.Context(), question, topK)
	if err != nil {
		api.logger.Warn("问答失败", "request_id", tc.RequestID, "err", err)
		status, message := http.StatusServiceUnavailable, "问答服务暂不可用，请稍后重试"
		if errors.Is(err, context.DeadlineExceeded) {
			status, message = http.StatusGatewayTimeout, "问答超时，请稍后重试"
		}
		api.writeError(response, tc.RequestID, status, message)
		return
	}
	citations := make([]askCitation, 0, len(answer.Citations))
	for _, citation := range answer.Citations {
		citations = append(citations, askCitation{Number: citation.Number, RecordingID: citation.RecordingID.String(),
			ChunkIndex: citation.ChunkIndex, Content: citation.Content, Distance: citation.Distance})
	}
	api.logger.Info("问答完成", "request_id", tc.RequestID, "retrieved", len(answer.Retrieved), "citations", len(citations),
		"insufficient", answer.Insufficient, "invalid_citations", answer.InvalidCitations,
		"prompt_tokens", answer.Usage.PromptTokens, "completion_tokens", answer.Usage.CompletionTokens,
		"retrieve_ms", answer.RetrieveMS, "latency_ms", answer.Elapsed.Milliseconds())
	api.writeJSON(response, http.StatusOK, askResponse{
		Answer: answer.Text, Insufficient: answer.Insufficient, Citations: citations, Retrieved: len(answer.Retrieved),
		PromptTokens: answer.Usage.PromptTokens, CompletionTokens: answer.Usage.CompletionTokens,
		LatencyMS: answer.Elapsed.Milliseconds(), RequestID: tc.RequestID,
	})
}
