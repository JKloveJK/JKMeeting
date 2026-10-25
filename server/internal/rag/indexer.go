package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/embedding"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

// ChunkRunes 是单块上限，远低于 bge-m3 的上下文与 llama-server 的 ubatch。
const ChunkRunes = 300

const maxAttempts = 3

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, int, error)
}

type jobStore interface {
	Claim(context.Context, uuid.UUID, time.Duration) (Claim, error)
	ReleaseForRetry(context.Context, uuid.UUID, uuid.UUID) error
	MarkFailed(context.Context, uuid.UUID, uuid.UUID, string) error
	MarkReadyWithChunks(context.Context, uuid.UUID, uuid.UUID, []Chunk, [][]float32, int) error
}

type recordingFinder interface {
	FindByID(context.Context, uuid.UUID) (recording.Recording, error)
}

type Indexer struct {
	store      jobStore
	recordings recordingFinder
	embedder   Embedder
	lease      time.Duration
}

func NewIndexer(store jobStore, recordings recordingFinder, embedder Embedder) *Indexer {
	return &Indexer{store: store, recordings: recordings, embedder: embedder, lease: 5 * time.Minute}
}

// EmbedChunks 切块并向量化；返回块、向量与 embedding 耗时。离线评测与 Worker 共用。
func EmbedChunks(ctx context.Context, embedder Embedder, transcript string, maxRunes int) ([]Chunk, [][]float32, time.Duration, error) {
	chunks := Split(transcript, maxRunes)
	if len(chunks) == 0 {
		return nil, nil, 0, nil
	}
	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		texts[i] = chunk.Content
	}
	started := time.Now()
	vectors, _, err := embedder.Embed(ctx, texts)
	return chunks, vectors, time.Since(started), err
}

func (indexer *Indexer) ProcessTask(ctx context.Context, task *asynq.Task) error {
	payload, err := queue.ParseIndexPayload(task)
	if err != nil {
		return asynq.SkipRetry
	}
	recordingID, err := uuid.Parse(payload.RecordingID)
	if err != nil {
		return asynq.SkipRetry
	}
	claim, err := indexer.store.Claim(ctx, recordingID, indexer.lease)
	if err != nil {
		return err
	}
	if !claim.Claimed {
		return nil
	}
	record, err := indexer.recordings.FindByID(ctx, recordingID)
	if err != nil {
		return indexer.retryOrFail(ctx, recordingID, claim, err)
	}
	transcript := ""
	if record.Transcript != nil {
		transcript = *record.Transcript
	}
	chunks, vectors, elapsed, err := EmbedChunks(ctx, indexer.embedder, transcript, ChunkRunes)
	if err != nil {
		slog.Warn("转写索引失败", "recording_id", recordingID, "request_id", payload.RequestID, "attempt", claim.Attempts, "err", err)
		if !embedding.IsRetryable(err) {
			if markErr := indexer.store.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(err)); markErr != nil {
				return markErr
			}
			return fmt.Errorf("%w：%v", asynq.SkipRetry, err)
		}
		return indexer.retryOrFail(ctx, recordingID, claim, err)
	}
	slog.Info("转写索引完成", "recording_id", recordingID, "request_id", payload.RequestID, "attempt", claim.Attempts,
		"chunks", len(chunks), "embed_ms", elapsed.Milliseconds())
	return indexer.store.MarkReadyWithChunks(ctx, recordingID, claim.LeaseToken, chunks, vectors, int(elapsed.Milliseconds()))
}

func (indexer *Indexer) retryOrFail(ctx context.Context, recordingID uuid.UUID, claim Claim, cause error) error {
	if claim.Attempts >= maxAttempts {
		if err := indexer.store.MarkFailed(ctx, recordingID, claim.LeaseToken, userError(cause)); err != nil {
			return err
		}
		return fmt.Errorf("%w：%v", asynq.SkipRetry, cause)
	}
	if err := indexer.store.ReleaseForRetry(ctx, recordingID, claim.LeaseToken); err != nil {
		return err
	}
	return fmt.Errorf("转写索引临时失败：%w", cause)
}

func userError(cause error) string {
	message := cause.Error()
	if errors.Is(cause, context.DeadlineExceeded) {
		message = "embedding 调用超时"
	}
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return "索引失败：" + string(runes)
}
