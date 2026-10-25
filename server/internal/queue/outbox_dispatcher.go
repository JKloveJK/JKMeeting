package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// TaskFactory 根据 outbox 事件构造要投递的 asynq 任务（含类型与队列）。
type TaskFactory func(recordingID, requestID string) (*asynq.Task, error)

type Dispatcher struct {
	outbox     outboxStore
	enqueuer   taskEnqueuer
	batchLimit int
	newTask    TaskFactory
	options    []asynq.Option
}

func NewDispatcher(outboxStore outboxStore, enqueuer taskEnqueuer) *Dispatcher {
	return &Dispatcher{outbox: outboxStore, enqueuer: enqueuer, batchLimit: 64, newTask: NewTranscriptionTask}
}

func NewTranscodeDispatcher(outboxStore outboxStore, enqueuer taskEnqueuer) *Dispatcher {
	return &Dispatcher{outbox: outboxStore, enqueuer: enqueuer, batchLimit: 64, newTask: NewTranscodeTask,
		options: TranscodeEnqueueOptions()}
}

func NewAnalysisDispatcher(outboxStore outboxStore, enqueuer taskEnqueuer) *Dispatcher {
	return &Dispatcher{outbox: outboxStore, enqueuer: enqueuer, batchLimit: 64, newTask: NewAnalysisTask,
		options: AnalysisEnqueueOptions()}
}

func NewIndexDispatcher(outboxStore outboxStore, enqueuer taskEnqueuer) *Dispatcher {
	return &Dispatcher{outbox: outboxStore, enqueuer: enqueuer, batchLimit: 64, newTask: NewIndexTask,
		options: IndexEnqueueOptions()}
}

func (dispatcher *Dispatcher) enqueueOptions(taskID string) []asynq.Option {
	return append([]asynq.Option{asynq.TaskID(taskID)}, dispatcher.options...)
}

// Dispatchers 依次派发多张 outbox；任一失败不阻断其余派发，错误合并返回。
type Dispatchers []*Dispatcher

func (dispatchers Dispatchers) DispatchAll(ctx context.Context) error {
	var errs []error
	for _, dispatcher := range dispatchers {
		if err := dispatcher.DispatchAll(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (dispatcher *Dispatcher) WithBatchLimit(limit int) *Dispatcher {
	if limit > 0 {
		dispatcher.batchLimit = limit
	}
	return dispatcher
}

func (dispatcher *Dispatcher) DispatchOne(ctx context.Context) error {
	event, err := dispatcher.outbox.ClaimPending(ctx)
	if err != nil || event == nil {
		return err
	}

	task, err := dispatcher.newTask(event.RecordingID.String(), event.RequestID)
	if err != nil {
		_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
		return err
	}
	if _, err := dispatcher.enqueuer.Enqueue(task, dispatcher.enqueueOptions(event.TaskID)...); err != nil && !isAlreadyEnqueued(err) {
		_ = dispatcher.outbox.RequeueIfDispatching(ctx, event.ID)
		return fmt.Errorf("投递异步任务失败：%w", err)
	}
	return dispatcher.outbox.MarkDispatched(ctx, event.ID)
}

// DispatchAll 在单次调用中按 batchLimit 上限持续派发 pending 事件，既防止单条历史事件阻塞新请求，又避免 10 万级积压在单次 HTTP 请求中无限循环引发雪崩。
func (dispatcher *Dispatcher) DispatchAll(ctx context.Context) error {
	_, err := dispatcher.DispatchBatch(ctx, dispatcher.batchLimit)
	return err
}

func (dispatcher *Dispatcher) DispatchBatch(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 64
	}
	dispatchedCount := 0
	for dispatchedCount < limit {
		event, err := dispatcher.outbox.ClaimPending(ctx)
		if err != nil {
			return dispatchedCount, err
		}
		if event == nil {
			return dispatchedCount, nil
		}

		task, err := dispatcher.newTask(event.RecordingID.String(), event.RequestID)
		if err != nil {
			dispatcher.requeueSafely(event.ID)
			return dispatchedCount, err
		}
		if _, err := dispatcher.enqueuer.Enqueue(task, dispatcher.enqueueOptions(event.TaskID)...); err != nil && !isAlreadyEnqueued(err) {
			dispatcher.requeueSafely(event.ID)
			return dispatchedCount, fmt.Errorf("投递异步任务失败：%w", err)
		}
		if err := dispatcher.outbox.MarkDispatched(ctx, event.ID); err != nil {
			return dispatchedCount, err
		}
		dispatchedCount++
	}
	return dispatchedCount, nil
}

// StartBackgroundLoop 定期在后台按批消化未投递的 Outbox 事件，确保 Redis 抖动恢复或积压时能平滑自愈。
func (dispatcher *Dispatcher) StartBackgroundLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := dispatcher.DispatchBatch(ctx, dispatcher.batchLimit); err != nil {
					slog.Warn("后台派发 Outbox 批次遇到临时错误", "err", err)
				}
			}
		}
	}()
}

func (dispatcher *Dispatcher) requeueSafely(eventID uuid.UUID) {
	requeueCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = dispatcher.outbox.RequeueIfDispatching(requeueCtx, eventID)
}

func isAlreadyEnqueued(err error) bool {
	return errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask)
}
