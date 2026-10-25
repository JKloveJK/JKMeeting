package transcribe

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type recordingStore interface {
	FindByID(context.Context, uuid.UUID) (recording.Recording, error)
	ClaimForTranscription(context.Context, uuid.UUID, time.Duration) (recording.TranscriptionClaim, error)
	ReleaseForRetryWithLease(context.Context, uuid.UUID, uuid.UUID) error
	MarkFailedWithLease(context.Context, uuid.UUID, uuid.UUID, string) error
	MarkDoneWithLease(context.Context, uuid.UUID, uuid.UUID, string, string) error
}

type downloader interface {
	DownloadToTemp(context.Context, string, int64) (string, error)
}

type Handler struct {
	recordings recordingStore
	downloads  downloader
	asr        *Client
	lease      time.Duration
}

func NewHandler(recordings recordingStore, downloads downloader, asr *Client) *Handler {
	return &Handler{recordings: recordings, downloads: downloads, asr: asr, lease: 5 * time.Minute}
}

func (handler *Handler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	payload, err := queue.ParseTranscriptionPayload(task)
	if err != nil {
		return asynq.SkipRetry
	}
	recordingID, err := uuid.Parse(payload.RecordingID)
	if err != nil {
		return asynq.SkipRetry
	}
	claim, err := handler.recordings.ClaimForTranscription(ctx, recordingID, handler.lease)
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
	path, err := handler.downloads.DownloadToTemp(ctx, record.ObjectKey, 32<<20)
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	defer os.Remove(path)

	started := time.Now()
	text, err := handler.asr.Transcribe(ctx, path, payload.RequestID)
	slog.Info("ASR 转写完成", "recording_id", recordingID, "request_id", payload.RequestID, "attempt", claim.Attempts, "asr_ms", time.Since(started).Milliseconds())
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	return handler.recordings.MarkDoneWithLease(ctx, recordingID, claim.LeaseToken, text, payload.RequestID)
}

func (handler *Handler) retryOrFail(ctx context.Context, recordingID uuid.UUID, claim recording.TranscriptionClaim, cause error) error {
	if claim.Attempts >= 3 {
		return handler.recordings.MarkFailedWithLease(ctx, recordingID, claim.LeaseToken, userError(cause))
	}
	if err := handler.recordings.ReleaseForRetryWithLease(ctx, recordingID, claim.LeaseToken); err != nil {
		return err
	}
	return fmt.Errorf("转写临时失败：%w", cause)
}

func userError(cause error) string {
	message := cause.Error()
	if len(message) > 200 {
		message = message[:200]
	}
	return "转写暂时失败，请稍后重试：" + message
}
