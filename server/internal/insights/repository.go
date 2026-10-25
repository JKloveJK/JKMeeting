package insights

import (
	"context"
	"encoding/json"
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

var ErrNotFound = errors.New("meeting insights not found")

type Record struct {
	RecordingID      uuid.UUID
	Status           Status
	Attempts         int
	Result           json.RawMessage
	Model            *string
	PromptTokens     *int
	CompletionTokens *int
	LLMCalls         *int
	AnalysisMS       *int
	ErrorMessage     *string
	UpdatedAt        time.Time
}

type Claim struct {
	Claimed    bool
	LeaseToken uuid.UUID
	Attempts   int
}

// Outcome 是一次成功抽取需要持久化的结果与开销。
type Outcome struct {
	Result           json.RawMessage
	Model            string
	PromptTokens     int
	CompletionTokens int
	LLMCalls         int
	AnalysisMS       int
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
		UPDATE meeting_insights
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
		UPDATE meeting_insights
		SET status = 'pending', lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token)
	return err
}

func (repository *Repository) MarkFailed(ctx context.Context, recordingID, token uuid.UUID, message string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE meeting_insights
		SET status = 'failed', error_message = $3, lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, message)
	return err
}

func (repository *Repository) MarkReady(ctx context.Context, recordingID, token uuid.UUID, outcome Outcome) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE meeting_insights
		SET status = 'ready', result = $3, model = $4, prompt_tokens = $5, completion_tokens = $6,
		    llm_calls = $7, analysis_ms = $8, error_message = NULL,
		    lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE recording_id = $1 AND status = 'processing' AND lease_token = $2`,
		recordingID, token, outcome.Result, outcome.Model, outcome.PromptTokens, outcome.CompletionTokens,
		outcome.LLMCalls, outcome.AnalysisMS)
	return err
}

func (repository *Repository) FindInsights(ctx context.Context, recordingID uuid.UUID) (Record, error) {
	var record Record
	err := repository.pool.QueryRow(ctx, `
		SELECT recording_id, status, attempts, result, model, prompt_tokens, completion_tokens,
		       llm_calls, analysis_ms, error_message, updated_at
		FROM meeting_insights WHERE recording_id = $1`, recordingID).Scan(
		&record.RecordingID, &record.Status, &record.Attempts, &record.Result, &record.Model,
		&record.PromptTokens, &record.CompletionTokens, &record.LLMCalls, &record.AnalysisMS,
		&record.ErrorMessage, &record.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}
