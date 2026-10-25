package insights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type store interface {
	Claim(context.Context, uuid.UUID, time.Duration) (Claim, error)
	ReleaseForRetry(context.Context, uuid.UUID, uuid.UUID) error
	MarkFailed(context.Context, uuid.UUID, uuid.UUID, string) error
	MarkReady(context.Context, uuid.UUID, uuid.UUID, Outcome) error
}

type recordingFinder interface {
	FindByID(context.Context, uuid.UUID) (recording.Recording, error)
}

type extractor interface {
	Extract(context.Context, string) (Extraction, error)
}

const maxAttempts = 3

type Handler struct {
	store      store
	recordings recordingFinder
	extractor  extractor
	model      string
	lease      time.Duration
}

func NewHandler(store store, recordings recordingFinder, extractor extractor, model string) *Handler {
	return &Handler{store: store, recordings: recordings, extractor: extractor, model: model, lease: 10 * time.Minute}
}

func (handler *Handler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	payload, err := queue.ParseAnalysisPayload(task)
	if err != nil {
		return asynq.SkipRetry
	}
	recordingID, err := uuid.Parse(payload.RecordingID)
	if err != nil {
		return asynq.SkipRetry
	}
	claim, err := handler.store.Claim(ctx, recordingID, handler.lease)
	if err != nil {
		return err
	}
	if !claim.Claimed {
		return nil
	}
	record, err := handler.recordings.FindByID(ctx, recordingID)
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	transcript := ""
	if record.Transcript != nil {
		transcript = *record.Transcript
	}

	extraction, err := handler.extractor.Extract(ctx, transcript)
	logArgs := []any{"recording_id", recordingID, "request_id", payload.RequestID, "attempt", claim.Attempts,
		"llm_calls", extraction.Calls, "llm_attempts", extraction.Attempts, "format_failures", extraction.FormatFailures,
		"prompt_tokens", extraction.Usage.PromptTokens, "completion_tokens", extraction.Usage.CompletionTokens,
		"chunks", extraction.Chunks, "analysis_ms", extraction.Elapsed.Milliseconds()}
	if err != nil {
		slog.Warn("会议纪要抽取失败", append(logArgs, "err", err)...)
		if !llm.IsRetryable(err) {
			if markErr := handler.store.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(err)); markErr != nil {
				return markErr
			}
			return fmt.Errorf("%w：%v", asynq.SkipRetry, err)
		}
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	result, err := json.Marshal(extraction.Insights)
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	slog.Info("会议纪要抽取完成", logArgs...)
	return handler.store.MarkReady(ctx, recordingID, claim.LeaseToken, Outcome{
		Result: result, Model: handler.model, PromptTokens: extraction.Usage.PromptTokens,
		CompletionTokens: extraction.Usage.CompletionTokens, LLMCalls: extraction.Calls,
		AnalysisMS: int(extraction.Elapsed.Milliseconds()),
	})
}

func (handler *Handler) retryOrFail(ctx context.Context, recordingID uuid.UUID, claim Claim, cause error) error {
	if claim.Attempts >= maxAttempts {
		if err := handler.store.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(cause)); err != nil {
			return err
		}
		return fmt.Errorf("%w：%v", asynq.SkipRetry, cause)
	}
	if err := handler.store.ReleaseForRetry(ctx, recordingID, claim.LeaseToken); err != nil {
		return err
	}
	return fmt.Errorf("纪要抽取临时失败：%w", cause)
}

func userError(cause error) string {
	message := cause.Error()
	if errors.Is(cause, context.DeadlineExceeded) {
		message = "LLM 调用超时"
	}
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return "纪要抽取失败：" + string(runes)
}
