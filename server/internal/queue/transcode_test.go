package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/outbox"
)

func TestTranscodeTaskRoundTrip(t *testing.T) {
	task, err := NewTranscodeTask("recording-1", "request-1")
	if err != nil {
		t.Fatalf("NewTranscodeTask() error = %v", err)
	}
	if task.Type() != TypeTranscode {
		t.Fatalf("type = %q", task.Type())
	}
	payload, err := ParseTranscodePayload(task)
	if err != nil || payload.RecordingID != "recording-1" || payload.RequestID != "request-1" {
		t.Fatalf("payload = %+v err = %v", payload, err)
	}
}

type recordingEnqueuer struct {
	taskType string
	queue    string
}

func (enqueuer *recordingEnqueuer) Enqueue(task *asynq.Task, options ...asynq.Option) (*asynq.TaskInfo, error) {
	enqueuer.taskType = task.Type()
	enqueuer.queue = "default"
	for _, option := range options {
		if option.Type() == asynq.QueueOpt {
			enqueuer.queue = option.Value().(string)
		}
	}
	return &asynq.TaskInfo{}, nil
}

func TestTranscodeDispatcherEnqueuesIntoTranscodeQueue(t *testing.T) {
	event := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcode:test", RequestID: "request-test"}
	store := &fakeOutboxStore{event: event}
	enqueuer := &recordingEnqueuer{}

	if err := NewTranscodeDispatcher(store, enqueuer).DispatchOne(context.Background()); err != nil {
		t.Fatalf("DispatchOne() error = %v", err)
	}
	if enqueuer.taskType != TypeTranscode || enqueuer.queue != QueueTranscode || !store.dispatched {
		t.Fatalf("enqueuer = %+v dispatched = %v", enqueuer, store.dispatched)
	}

	transcription := &recordingEnqueuer{}
	if err := NewDispatcher(&fakeOutboxStore{event: event}, transcription).DispatchOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transcription.taskType != TypeTranscription || transcription.queue != "default" {
		t.Fatalf("transcription enqueuer = %+v", transcription)
	}
}

type failingOutboxStore struct{ fakeOutboxStore }

func (failingOutboxStore) ClaimPending(context.Context) (*outbox.Event, error) {
	return nil, errors.New("db down")
}

func TestDispatchersRunAllAndJoinErrors(t *testing.T) {
	event := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcode:test", RequestID: "request-test"}
	healthy := &fakeOutboxStore{event: event}
	dispatchers := Dispatchers{
		NewDispatcher(&failingOutboxStore{}, &fakeEnqueuer{}),
		NewTranscodeDispatcher(healthy, &fakeEnqueuer{}).WithBatchLimit(1),
	}
	if err := dispatchers.DispatchAll(context.Background()); err == nil {
		t.Fatal("DispatchAll() error = nil, want joined error")
	}
	if !healthy.dispatched {
		t.Fatal("second dispatcher must still run after the first fails")
	}
}
