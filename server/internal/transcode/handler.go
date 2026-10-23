package transcode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type jobStore interface {
	Claim(context.Context, uuid.UUID, time.Duration) (Claim, error)
	ReleaseForRetry(context.Context, uuid.UUID, uuid.UUID) error
	MarkFailed(context.Context, uuid.UUID, uuid.UUID, string) error
	MarkReady(context.Context, uuid.UUID, uuid.UUID, string, string, int) error
}

type recordingFinder interface {
	FindByID(context.Context, uuid.UUID) (recording.Recording, error)
}

type objectStore interface {
	DownloadToTemp(context.Context, string, int64) (string, error)
	UploadFile(context.Context, string, string, string) error
}

type runner interface {
	Run(ctx context.Context, input, outDir string, ladder []Variant) error
}

const maxAttempts = 3

var contentTypes = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl",
	".mp4":  "audio/mp4",
	".m4s":  "audio/mp4",
}

type Handler struct {
	jobs       jobStore
	recordings recordingFinder
	objects    objectStore
	runner     runner
	ladder     []Variant
	lease      time.Duration
}

func NewHandler(jobs jobStore, recordings recordingFinder, objects objectStore, runner runner, ladder []Variant) *Handler {
	return &Handler{jobs: jobs, recordings: recordings, objects: objects, runner: runner, ladder: ladder, lease: 10 * time.Minute}
}

// HLSPrefix 是录音 HLS 产物在对象存储中的目录前缀。
func HLSPrefix(recordingID uuid.UUID) string {
	return "recordings/" + recordingID.String() + "/hls/"
}

func (handler *Handler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	payload, err := queue.ParseTranscodePayload(task)
	if err != nil {
		return asynq.SkipRetry
	}
	recordingID, err := uuid.Parse(payload.RecordingID)
	if err != nil {
		return asynq.SkipRetry
	}
	claim, err := handler.jobs.Claim(ctx, recordingID, handler.lease)
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
	input, err := handler.objects.DownloadToTemp(ctx, record.ObjectKey, 32<<20)
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	defer os.Remove(input)
	outDir, err := os.MkdirTemp("", "jkmeeting-hls-*")
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	defer os.RemoveAll(outDir)

	started := time.Now()
	if err := handler.runner.Run(ctx, input, outDir, handler.ladder); err != nil {
		var ffmpegErr *FFmpegError
		if errors.Is(err, ErrFFmpegNotFound) || errors.As(err, &ffmpegErr) {
			slog.Warn("HLS 转码失败（不重试）", "recording_id", recordingID, "request_id", payload.RequestID, "err", err)
			if markErr := handler.jobs.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(err)); markErr != nil {
				return markErr
			}
			return fmt.Errorf("%w：%v", asynq.SkipRetry, err)
		}
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	transcodeMS := int(time.Since(started).Milliseconds())

	prefix := HLSPrefix(recordingID)
	uploaded, err := handler.uploadDir(ctx, outDir, prefix)
	if err != nil {
		return handler.retryOrFail(ctx, recordingID, claim, err)
	}
	rtf := 0.0
	if record.DurationMS > 0 {
		rtf = float64(transcodeMS) / float64(record.DurationMS)
	}
	slog.Info("HLS 转码完成", "recording_id", recordingID, "request_id", payload.RequestID, "attempt", claim.Attempts,
		"transcode_ms", transcodeMS, "audio_ms", record.DurationMS, "rtf", fmt.Sprintf("%.4f", rtf), "objects", uploaded)
	return handler.jobs.MarkReady(ctx, recordingID, claim.LeaseToken, prefix+"master.m3u8", LadderString(handler.ladder), transcodeMS)
}

func (handler *Handler) uploadDir(ctx context.Context, dir, prefix string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		contentType, ok := contentTypes[filepath.Ext(file)]
		if !ok {
			return fmt.Errorf("未预期的转码产物：%s", relative)
		}
		count++
		return handler.objects.UploadFile(ctx, prefix+path.Clean(filepath.ToSlash(relative)), file, contentType)
	})
	return count, err
}

func (handler *Handler) retryOrFail(ctx context.Context, recordingID uuid.UUID, claim Claim, cause error) error {
	if claim.Attempts >= maxAttempts {
		if err := handler.jobs.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(cause)); err != nil {
			return err
		}
		return fmt.Errorf("%w：%v", asynq.SkipRetry, cause)
	}
	if err := handler.jobs.ReleaseForRetry(ctx, recordingID, claim.LeaseToken); err != nil {
		return err
	}
	return fmt.Errorf("转码临时失败：%w", cause)
}

func userError(cause error) string {
	return "转码失败：" + tail(cause.Error(), 200)
}
