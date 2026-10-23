package queue

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/outbox"
)

type fakeOutboxStore struct {
	event      *outbox.Event
	dispatched bool
	requeued   bool
}

func (store *fakeOutboxStore) ClaimPending(context.Context) (*outbox.Event, error) {
	return store.event, nil
}
func (store *fakeOutboxStore) MarkDispatched(context.Context, uuid.UUID) error {
	store.dispatched = true
	return nil
}
func (store *fakeOutboxStore) RequeueIfDispatching(context.Context, uuid.UUID) error {
	store.requeued = true
	return nil
}

type fakeEnqueuer struct {
	taskID string
}

func (enqueuer *fakeEnqueuer) Enqueue(task *asynq.Task, options ...asynq.Option) (*asynq.TaskInfo, error) {
	for _, option := range options {
		_ = option
	}
	enqueuer.taskID = task.Type()
	return &asynq.TaskInfo{}, nil
}

func TestDispatchOneMarksClaimedEventDispatched(t *testing.T) {
	event := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcription:test", RequestID: "request-test", Status: outbox.StatusDispatching}
	store := &fakeOutboxStore{event: event}
	dispatcher := NewDispatcher(store, &fakeEnqueuer{})

	if err := dispatcher.DispatchOne(context.Background()); err != nil {
		t.Fatalf("DispatchOne() error = %v", err)
	}
	if !store.dispatched {
		t.Fatal("MarkDispatched() was not called")
	}
}

type sequentialOutboxStore struct {
	events      []*outbox.Event
	dispatched  []uuid.UUID
}

func (store *sequentialOutboxStore) ClaimPending(context.Context) (*outbox.Event, error) {
	if len(store.events) == 0 {
		return nil, nil
	}
	event := store.events[0]
	store.events = store.events[1:]
	return event, nil
}

func (store *sequentialOutboxStore) MarkDispatched(_ context.Context, eventID uuid.UUID) error {
	store.dispatched = append(store.dispatched, eventID)
	return nil
}

func (store *sequentialOutboxStore) RequeueIfDispatching(context.Context, uuid.UUID) error {
	return nil
}

func TestDispatchAllDrainsOlderPendingEventsBeforeReturning(t *testing.T) {
	olderEvent := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcription:older", RequestID: "request-older", Status: outbox.StatusDispatching}
	newerEvent := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcription:newer", RequestID: "request-newer", Status: outbox.StatusDispatching}
	store := &sequentialOutboxStore{events: []*outbox.Event{olderEvent, newerEvent}}
	dispatcher := NewDispatcher(store, &fakeEnqueuer{})

	if err := dispatcher.DispatchAll(context.Background()); err != nil {
		t.Fatalf("DispatchAll() error = %v", err)
	}
	if len(store.dispatched) != 2 {
		t.Fatalf("dispatched count = %d, want 2", len(store.dispatched))
	}
	if store.dispatched[1] != newerEvent.ID {
		t.Fatalf("newer event was not dispatched last: got %s, want %s", store.dispatched[1], newerEvent.ID)
	}
}

type conflictEnqueuer struct{}

func (conflictEnqueuer) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, asynq.ErrTaskIDConflict
}

func TestDispatchOneMarksDispatchedWhenTaskAlreadyEnqueued(t *testing.T) {
	event := &outbox.Event{ID: uuid.New(), RecordingID: uuid.New(), TaskID: "transcription:conflict", RequestID: "request-conflict", Status: outbox.StatusDispatching}
	store := &fakeOutboxStore{event: event}
	dispatcher := NewDispatcher(store, conflictEnqueuer{})

	if err := dispatcher.DispatchOne(context.Background()); err != nil {
		t.Fatalf("DispatchOne() error = %v", err)
	}
	if !store.dispatched {
		t.Fatal("MarkDispatched() was not called on ErrTaskIDConflict")
	}
	if store.requeued {
		t.Fatal("RequeueIfDispatching() should not be called on ErrTaskIDConflict")
	}
}
