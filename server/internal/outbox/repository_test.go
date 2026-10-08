package outbox

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClaimPendingClaimsOnlyOnce(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL 未设置")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DELETE FROM transcription_outbox"); err != nil {
		t.Fatalf("clear outbox error = %v", err)
	}

	recordingID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO recordings (id, user_id, status, object_key, duration_ms, size_bytes, content_type)
		VALUES ($1, 'p0-local-user', 'uploaded', $2, 1000, 4, 'audio/mp4')`,
		recordingID,
		"recordings/"+recordingID.String()+"/audio.m4a",
	)
	if err != nil {
		t.Fatalf("insert recording error = %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO transcription_outbox (id, recording_id, task_id, request_id, status)
		VALUES ($1, $2, $3, 'request-test', 'pending')`,
		uuid.New(), recordingID, "transcription:"+recordingID.String())
	if err != nil {
		t.Fatalf("insert outbox error = %v", err)
	}

	repository := NewRepository(pool)
	claimed, err := repository.ClaimPending(ctx)
	if err != nil {
		t.Fatalf("ClaimPending() error = %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimPending() = nil, want pending event")
	}
	if claimed.Status != StatusDispatching {
		t.Fatalf("claimed status = %q, want %q", claimed.Status, StatusDispatching)
	}

	_, err = repository.ClaimPending(ctx)
	if err != nil {
		t.Fatalf("second ClaimPending() error = %v", err)
	}

	var ownStatus string
	if err := pool.QueryRow(ctx, "SELECT status FROM transcription_outbox WHERE id = $1", claimed.ID).Scan(&ownStatus); err != nil {
		t.Fatalf("query claimed event error = %v", err)
	}
	if ownStatus != string(StatusDispatching) {
		t.Fatalf("claimed event status = %q, want %q", ownStatus, StatusDispatching)
	}

	if err := repository.MarkDispatched(ctx, claimed.ID); err != nil {
		t.Fatalf("MarkDispatched() error = %v", err)
	}
}
