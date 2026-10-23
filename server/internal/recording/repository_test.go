package recording

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClaimForTranscriptionUsesLeaseToken(t *testing.T) {
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

	repository := NewRepository(pool)
	recordingID := uuid.New()
	if err := repository.Create(ctx, Recording{ID: recordingID, UserID: "p0-local-user", ObjectKey: "recordings/test/audio.m4a", DurationMS: 1000, SizeBytes: 4, ContentType: "audio/mp4"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE recordings SET status = 'uploaded' WHERE id = $1", recordingID); err != nil {
		t.Fatalf("set uploaded error = %v", err)
	}

	claimed, err := repository.ClaimForTranscription(ctx, recordingID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimForTranscription() error = %v", err)
	}
	if !claimed.Claimed || claimed.LeaseToken == uuid.Nil {
		t.Fatalf("claim = %+v, want lease token", claimed)
	}
	if err := repository.MarkDoneWithLease(ctx, recordingID, uuid.New(), "stale result"); err != nil {
		t.Fatalf("stale MarkDoneWithLease() error = %v", err)
	}
	loaded, err := repository.FindByID(ctx, recordingID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if loaded.Status != StatusTranscribing {
		t.Fatalf("status after stale token = %q, want %q", loaded.Status, StatusTranscribing)
	}
	if err := repository.MarkDoneWithLease(ctx, recordingID, claimed.LeaseToken, "final text"); err != nil {
		t.Fatalf("MarkDoneWithLease() error = %v", err)
	}
}

func TestMarkUploadedAndCreateOutboxIsAtomic(t *testing.T) {
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

	repository := NewRepository(pool)
	recordingID := uuid.New()
	recording := Recording{
		ID:          recordingID,
		UserID:      "p0-local-user",
		ObjectKey:   "recordings/" + recordingID.String() + "/audio.m4a",
		DurationMS:  1_000,
		SizeBytes:   4,
		ContentType: "audio/mp4",
	}
	if err := repository.Create(ctx, recording); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	requestID := "request-test"
	updated, err := repository.MarkUploadedAndCreateOutbox(ctx, recordingID, requestID)
	if err != nil {
		t.Fatalf("MarkUploadedAndCreateOutbox() error = %v", err)
	}
	if !updated {
		t.Fatal("MarkUploadedAndCreateOutbox() updated = false, want true")
	}

	loaded, err := repository.FindByID(ctx, recordingID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if loaded.Status != StatusUploaded {
		t.Fatalf("status = %q, want %q", loaded.Status, StatusUploaded)
	}

	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM transcription_outbox WHERE recording_id = $1 AND request_id = $2`, recordingID, requestID).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox error = %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("outbox count = %d, want 1", outboxCount)
	}
	var jobStatus string
	var transcodeOutbox int
	if err := pool.QueryRow(ctx, `
		SELECT j.status, (SELECT count(*) FROM transcode_outbox o WHERE o.recording_id = j.recording_id AND o.request_id = $2)
		FROM transcode_jobs j WHERE j.recording_id = $1`, recordingID, requestID).Scan(&jobStatus, &transcodeOutbox); err != nil {
		t.Fatalf("query transcode job error = %v", err)
	}
	if jobStatus != "pending" || transcodeOutbox != 1 {
		t.Fatalf("transcode job = %q outbox = %d, want pending and 1", jobStatus, transcodeOutbox)
	}

	updated, err = repository.MarkUploadedAndCreateOutbox(ctx, recordingID, requestID)
	if err != nil {
		t.Fatalf("second MarkUploadedAndCreateOutbox() error = %v", err)
	}
	if updated {
		t.Fatal("second MarkUploadedAndCreateOutbox() updated = true, want false")
	}

	if err := repository.MarkFailedIfUploaded(ctx, recordingID, "worker failed", time.Now()); err != nil {
		t.Fatalf("MarkFailedIfUploaded() error = %v", err)
	}
}
