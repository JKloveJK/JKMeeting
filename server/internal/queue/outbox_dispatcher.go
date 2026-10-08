package queue

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/outbox"
)

type outboxStore interface {
	ClaimPending(context.Context) (*outbox.Event, error)
	MarkDispatched(context.Context, uuid.UUID) error
	RequeueIfDispatching(context.Context, uuid.UUID) error
}

type taskEnqueuer interface {
	Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

type Dispatcher struct {
	outbox   outboxStore
	enqueuer taskEnqueuer
}

func NewDispatcher(outboxStore outboxStore, enqueuer taskEnqueuer) *Dispatcher {
	return &Dispatcher{outbox: outboxStore, enqueuer: enqueuer}
}

func (dispatcher *Dispatcher) DispatchOne(ctx context.Context) error {
	event, err := dispatcher.outbox.ClaimPending(ctx)
	if err != nil || event == nil {
		return err
	}

	task, err := NewTranscriptionTask(event.RecordingID.String(), event.RequestID)
	if err != nil {
		_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
		return err
	}
	if _, err := dispatcher.enqueuer.Enqueue(task, asynq.TaskID(event.TaskID)); err != nil {
		_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
		return fmt.Errorf("投递转写任务失败：%w", err)
	}
	return dispatcher.outbox.MarkDispatched(ctx, event.ID)
}

// DispatchAll 持续派发 pending 事件，直到 outbox 已清空。这样旧事件不能阻塞新上传录音进入 Redis。
func (dispatcher *Dispatcher) DispatchAll(ctx context.Context) error {
	for {
		event, err := dispatcher.outbox.ClaimPending(ctx)
		if err != nil {
			return err
		}
		if event == nil {
			return nil
		}

		task, err := NewTranscriptionTask(event.RecordingID.String(), event.RequestID)
		if err != nil {
			_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
			return err
		}
		if _, err := dispatcher.enqueuer.Enqueue(task, asynq.TaskID(event.TaskID)); err != nil {
			_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
			return fmt.Errorf("投递转写任务失败：%w", err)
		}
		if err := dispatcher.outbox.MarkDispatched(ctx, event.ID); err != nil {
			return err
		}
	}
}
