package outbox

import (
	"context"
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

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) ClaimPending(ctx context.Context) (*Event, error) {
	const query = `
		WITH next_event AS (
			SELECT id
			FROM transcription_outbox
			WHERE status = 'pending'
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE transcription_outbox AS outbox
		SET status = 'dispatching', claimed_at = now(), updated_at = now()
		FROM next_event
		WHERE outbox.id = next_event.id AND outbox.status = 'pending'
		RETURNING outbox.id, outbox.recording_id, outbox.task_id, outbox.request_id,
		          outbox.status, outbox.claimed_at`

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
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcription_outbox
		SET status = 'dispatched', dispatched_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'dispatching'`, eventID)
	return err
}

func (repository *Repository) RequeueIfDispatching(ctx context.Context, eventID uuid.UUID) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcription_outbox
		SET status = 'pending', claimed_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'dispatching'`, eventID)
	return err
}
