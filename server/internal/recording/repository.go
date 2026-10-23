package recording

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) Create(ctx context.Context, recording Recording) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO recordings (id, user_id, status, object_key, duration_ms, size_bytes, content_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		recording.ID,
		recording.UserID,
		StatusCreated,
		recording.ObjectKey,
		recording.DurationMS,
		recording.SizeBytes,
		recording.ContentType,
	)
	return err
}

func (repository *Repository) MarkUploadedAndCreateOutbox(ctx context.Context, recordingID uuid.UUID, requestID string) (bool, error) {
	transaction, err := repository.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	result, err := transaction.Exec(ctx, `
		UPDATE recordings
		SET status = $2, updated_at = now()
		WHERE id = $1 AND status = $3`,
		recordingID,
		StatusUploaded,
		StatusCreated,
	)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() != 1 {
		return false, transaction.Commit(ctx)
	}

	_, err = transaction.Exec(ctx, `
		INSERT INTO transcription_outbox (id, recording_id, task_id, request_id, status)
		VALUES ($1, $2, $3, $4, 'pending')`,
		uuid.New(),
		recordingID,
		"transcription:"+recordingID.String(),
		requestID,
	)
	if err != nil {
		return false, err
	}

	// 转码与转写各自一条 outbox，互不阻塞；三条写入与状态变更同属一个事务。
	if _, err = transaction.Exec(ctx, `
		INSERT INTO transcode_jobs (recording_id, status) VALUES ($1, 'pending')`,
		recordingID,
	); err != nil {
		return false, err
	}
	if _, err = transaction.Exec(ctx, `
		INSERT INTO transcode_outbox (id, recording_id, task_id, request_id, status)
		VALUES ($1, $2, $3, $4, 'pending')`,
		uuid.New(),
		recordingID,
		"transcode:"+recordingID.String(),
		requestID,
	); err != nil {
		return false, err
	}
	return true, transaction.Commit(ctx)
}

func (repository *Repository) ClaimForTranscription(ctx context.Context, recordingID uuid.UUID, leaseDuration time.Duration) (TranscriptionClaim, error) {
	leaseToken := uuid.New()
	var attempts int
	err := repository.pool.QueryRow(ctx, `
		UPDATE recordings
		SET status = $2,
		    transcribe_attempts = transcribe_attempts + 1,
		    transcribe_lease_token = $3,
		    transcribe_lease_until = now() + $4::interval,
		    updated_at = now()
		WHERE id = $1
		  AND (status = $5 OR (status = $2 AND transcribe_lease_until < now()))
		RETURNING transcribe_attempts`,
		recordingID,
		StatusTranscribing,
		leaseToken,
		leaseDuration.String(),
		StatusUploaded,
	).Scan(&attempts)
	if err == pgx.ErrNoRows {
		return TranscriptionClaim{}, nil
	}
	if err != nil {
		return TranscriptionClaim{}, err
	}
	return TranscriptionClaim{Claimed: true, LeaseToken: leaseToken, Attempts: attempts}, nil
}

func (repository *Repository) ReleaseForRetryWithLease(ctx context.Context, recordingID, leaseToken uuid.UUID) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE recordings
		SET status = $3, transcribe_lease_token = NULL, transcribe_lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = $2 AND transcribe_lease_token = $4`,
		recordingID, StatusTranscribing, StatusUploaded, leaseToken)
	return err
}

func (repository *Repository) MarkFailedWithLease(ctx context.Context, recordingID, leaseToken uuid.UUID, message string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE recordings
		SET status = $3, error_message = $4, transcribe_lease_token = NULL, transcribe_lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = $2 AND transcribe_lease_token = $5`,
		recordingID, StatusTranscribing, StatusFailed, message, leaseToken)
	return err
}

func (repository *Repository) MarkDoneWithLease(ctx context.Context, recordingID, leaseToken uuid.UUID, transcript string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE recordings
		SET status = $3, transcript = $4, error_message = NULL,
		    transcribe_lease_token = NULL, transcribe_lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = $2 AND transcribe_lease_token = $5`,
		recordingID,
		StatusTranscribing,
		StatusDone,
		transcript,
		leaseToken,
	)
	return err
}

func (repository *Repository) FindByID(ctx context.Context, recordingID uuid.UUID) (Recording, error) {
	var recording Recording
	err := repository.pool.QueryRow(ctx, `
		SELECT id, user_id, status, object_key, duration_ms, size_bytes, content_type,
		       transcript, error_message, created_at, updated_at
		FROM recordings WHERE id = $1`, recordingID).Scan(
		&recording.ID,
		&recording.UserID,
		&recording.Status,
		&recording.ObjectKey,
		&recording.DurationMS,
		&recording.SizeBytes,
		&recording.ContentType,
		&recording.Transcript,
		&recording.ErrorMessage,
		&recording.CreatedAt,
		&recording.UpdatedAt,
	)
	return recording, err
}

func (repository *Repository) MarkFailedIfUploaded(ctx context.Context, recordingID uuid.UUID, message string, _ time.Time) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE recordings
		SET status = $2, error_message = $3, updated_at = now()
		WHERE id = $1 AND status = $4`,
		recordingID,
		StatusFailed,
		message,
		StatusUploaded,
	)
	return err
}

func IsNotFound(err error) bool {
	return err == pgx.ErrNoRows
}
