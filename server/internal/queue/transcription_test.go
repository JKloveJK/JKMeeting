package queue

import "testing"

func TestNewTranscriptionTaskCarriesRecordingAndRequestIDs(t *testing.T) {
	task, err := NewTranscriptionTask("recording-1", "request-1")
	if err != nil {
		t.Fatalf("NewTranscriptionTask() error = %v", err)
	}
	if task.Type() != TypeTranscription {
		t.Fatalf("Type() = %q, want %q", task.Type(), TypeTranscription)
	}

	payload, err := ParseTranscriptionPayload(task)
	if err != nil {
		t.Fatalf("ParseTranscriptionPayload() error = %v", err)
	}
	if payload.RecordingID != "recording-1" || payload.RequestID != "request-1" {
		t.Fatalf("payload = %+v, want recording and request ids", payload)
	}
}
