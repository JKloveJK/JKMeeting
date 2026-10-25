package rag

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Claim struct {
	Claimed    bool
	LeaseToken uuid.UUID
	Attempts   int
}

type Hit struct {
	ChunkID     int64
	RecordingID uuid.UUID
	ChunkIndex  int
	Content     string
	Distance    float64
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) Claim(ctx context.Context, recordingID uuid.UUID, lease time.Duration) (Claim, error) {
	token := uuid.New()
	var attempts int
	err := repository.pool.QueryRow(ctx, `
		UPDATE transcript_index_jobs
		SET status = 'processing', attempts = attempts + 1, lease_token = $2,
		    lease_until = now() + $3::interval, updated_at = now()
		WHERE recording_id = $1
		  AND (status = 'pending' OR (status = 'processing' AND lease_until < now()))
		RETURNING attempts`,
		recordingID, token, lease.String(),
	).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, err
	}
	return Claim{Claimed: true, LeaseToken: token, Attempts: attempts}, nil
}

func (repository *Repository) ReleaseForRetry(ctx context.Context, recordingID, token uuid.UUID) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcript_index_jobs
		SET status = 'pending', lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token)
	return err
}

func (repository *Repository) MarkFailed(ctx context.Context, recordingID, token uuid.UUID, message string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcript_index_jobs
		SET status = 'failed', error_message = $3, lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, message)
	return err
}

// MarkReadyWithChunks 在持有租约时，于同一事务内替换该录音的全部块并把任务标记为 ready。
func (repository *Repository) MarkReadyWithChunks(ctx context.Context, recordingID, token uuid.UUID, chunks []Chunk, vectors [][]float32, embedMS int) error {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `
		UPDATE transcript_index_jobs
		SET status = 'ready', chunks = $3, embed_ms = $4, error_message = NULL,
		    lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, len(chunks), embedMS)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return nil
	}
	if err := replaceChunks(ctx, transaction, recordingID, chunks, vectors); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// ReplaceChunks 不经过任务租约直接重建索引，供离线评测使用。
func (repository *Repository) ReplaceChunks(ctx context.Context, recordingID uuid.UUID, chunks []Chunk, vectors [][]float32) error {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := replaceChunks(ctx, transaction, recordingID, chunks, vectors); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func replaceChunks(ctx context.Context, transaction pgx.Tx, recordingID uuid.UUID, chunks []Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return errors.New("块与向量数量不一致")
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM transcript_chunks WHERE recording_id = $1`, recordingID); err != nil {
		return err
	}
	for i, chunk := range chunks {
		if _, err := transaction.Exec(ctx, `
			INSERT INTO transcript_chunks (recording_id, chunk_index, content, embedding)
			VALUES ($1, $2, $3, $4::vector)`,
			recordingID, chunk.Index, chunk.Content, VectorLiteral(vectors[i])); err != nil {
			return err
		}
	}
	return nil
}

// Search 按余弦距离升序返回最近的 k 个块。
func (repository *Repository) Search(ctx context.Context, vector []float32, k int) ([]Hit, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT id, recording_id, chunk_index, content, embedding <=> $1::vector AS distance
		FROM transcript_chunks
		ORDER BY embedding <=> $1::vector
		LIMIT $2`, VectorLiteral(vector), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var hit Hit
		if err := rows.Scan(&hit.ChunkID, &hit.RecordingID, &hit.ChunkIndex, &hit.Content, &hit.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// VectorLiteral 生成 pgvector 文本格式 "[x,y,...]"，避免为一个类型引入额外驱动依赖。
func VectorLiteral(vector []float32) string {
	var builder strings.Builder
	builder.Grow(len(vector) * 10)
	builder.WriteByte('[')
	for i, value := range vector {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}
