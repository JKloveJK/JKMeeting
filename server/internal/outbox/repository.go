package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusDispatching Status = "dispatching"
	StatusDispatched  Status = "dispatched"
)

type Event struct {
	ID          uuid.UUID
	RecordingID uuid.UUID
	TaskID      string
	RequestID   string
	Status      Status
	ClaimedAt   *time.Time
}

const (
	TranscriptionTable = "transcription_outbox"
	TranscodeTable     = "transcode_outbox"
)

// Repository 操作一张 outbox 表；表名只能取上面的常量，SQL 中不拼接外部输入。
type Repository struct {
	pool  *pgxpool.Pool
	table string
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, table: TranscriptionTable}
}

func NewTranscodeRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, table: TranscodeTable}
}

func (repository *Repository) Table() string { return repository.table }

func (repository *Repository) ClaimPending(ctx context.Context) (*Event, error) {
	query := fmt.Sprintf(`
		WITH next_event AS (
			SELECT id
			FROM %[1]s
			WHERE status = 'pending'
			   OR (status = 'dispatching' AND claimed_at < now() - interval '1 minute')
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE %[1]s AS outbox
		SET status = 'dispatching', claimed_at = now(), updated_at = now()
		FROM next_event
		WHERE outbox.id = next_event.id
		  AND (outbox.status = 'pending' OR (outbox.status = 'dispatching' AND outbox.claimed_at < now() - interval '1 minute'))
		RETURNING outbox.id, outbox.recording_id, outbox.task_id, outbox.request_id,
		          outbox.status, outbox.claimed_at`, repository.table)

	var event Event
	err := repository.pool.QueryRow(ctx, query).Scan(
		&event.ID,
		&event.RecordingID,
		&event.TaskID,
		&event.RequestID,
		&event.Status,
		&event.ClaimedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (repository *Repository) MarkDispatched(ctx context.Context, eventID uuid.UUID) error {
	_, err := repository.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s
		SET status = 'dispatched', dispatched_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'dispatching'`, repository.table), eventID)
	return err
}

func (repository *Repository) RequeueIfDispatching(ctx context.Context, eventID uuid.UUID) error {
	_, err := repository.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s
		SET status = 'pending', claimed_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'dispatching'`, repository.table), eventID)
	return err
}

func (repository *Repository) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := repository.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*)
		FROM %s
		WHERE status IN ('pending', 'dispatching')`, repository.table)).Scan(&count)
	return count, err
}
