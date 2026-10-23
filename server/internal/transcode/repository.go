package transcode

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

var ErrJobNotFound = errors.New("transcode job not found")

type Job struct {
	RecordingID  uuid.UUID
	Status       Status
	Attempts     int
	MasterKey    *string
	Variants     *string
	TranscodeMS  *int
	ErrorMessage *string
	UpdatedAt    time.Time
}

type Claim struct {
	Claimed    bool
	LeaseToken uuid.UUID
	Attempts   int
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Claim 领取 pending 或租约已过期的 processing 任务；未领取到时 Claimed 为 false。
func (repository *Repository) Claim(ctx context.Context, recordingID uuid.UUID, lease time.Duration) (Claim, error) {
	token := uuid.New()
	var attempts int
	err := repository.pool.QueryRow(ctx, `
		UPDATE transcode_jobs
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
		UPDATE transcode_jobs
		SET status = 'pending', lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token)
	return err
}

func (repository *Repository) MarkFailed(ctx context.Context, recordingID, token uuid.UUID, message string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcode_jobs
		SET status = 'failed', error_message = $3, lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, message)
	return err
}

func (repository *Repository) MarkReady(ctx context.Context, recordingID, token uuid.UUID, masterKey, variants string, transcodeMS int) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE transcode_jobs
		SET status = 'ready', master_key = $3, variants = $4, transcode_ms = $5, error_message = NULL,
		    lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, masterKey, variants, transcodeMS)
	return err
}

func (repository *Repository) FindTranscodeJob(ctx context.Context, recordingID uuid.UUID) (Job, error) {
	var job Job
	err := repository.pool.QueryRow(ctx, `
		SELECT recording_id, status, attempts, master_key, variants, transcode_ms, error_message, updated_at
		FROM transcode_jobs WHERE recording_id = $1`, recordingID).Scan(
		&job.RecordingID, &job.Status, &job.Attempts, &job.MasterKey, &job.Variants,
		&job.TranscodeMS, &job.ErrorMessage, &job.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	return job, err
}
