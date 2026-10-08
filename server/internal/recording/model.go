package recording

import (
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusCreated      Status = "created"
	StatusUploaded     Status = "uploaded"
	StatusTranscribing Status = "transcribing"
	StatusDone         Status = "done"
	StatusFailed       Status = "failed"
)

type Recording struct {
	ID           uuid.UUID
	UserID       string
	Status       Status
	ObjectKey    string
	DurationMS   int
	SizeBytes    int64
	ContentType  string
	Transcript   *string
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt           time.Time
	TranscribeAttempts  int
	TranscribeLeaseToken *uuid.UUID
	TranscribeLeaseUntil *time.Time
}

type TranscriptionClaim struct {
	Claimed    bool
	LeaseToken uuid.UUID
	Attempts   int
}
