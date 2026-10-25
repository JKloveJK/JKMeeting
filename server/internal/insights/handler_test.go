package insights

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type fakeStore struct {
	claimed   bool
	attempts  int
	released  bool
	failedMsg string
	ready     *Outcome
}

func (store *fakeStore) Claim(context.Context, uuid.UUID, time.Duration) (Claim, error) {
	if !store.claimed {
		return Claim{}, nil
	}
	return Claim{Claimed: true, LeaseToken: uuid.New(), Attempts: store.attempts}, nil
}
func (store *fakeStore) ReleaseForRetry(context.Context, uuid.UUID, uuid.UUID) error {
	store.released = true
	return nil
}
func (store *fakeStore) MarkFailed(_ context.Context, _, _ uuid.UUID, message string) error {
	store.failedMsg = message
	return nil
}
func (store *fakeStore) MarkReady(_ context.Context, _, _ uuid.UUID, outcome Outcome) error {
	store.ready = &outcome
	return nil
}

type fakeRecordings struct{ transcript *string }

func (recordings fakeRecordings) FindByID(context.Context, uuid.UUID) (recording.Recording, error) {
	return recording.Recording{Transcript: recordings.transcript}, nil
}

type fakeExtractor struct {
	got string
	err error
}

func (extractor *fakeExtractor) Extract(_ context.Context, transcript string) (Extraction, error) {
	extractor.got = transcript
	if extractor.err != nil {
		return Extraction{Calls: 1, Attempts: 3}, extractor.err
	}
	parsed, _ := Parse([]byte(validJSON))
	return Extraction{Insights: parsed, Calls: 1, Attempts: 1, Usage: llm.Usage{PromptTokens: 300, CompletionTokens: 80},
		Elapsed: 1500 * time.Millisecond}, nil
}

func analysisTask(t *testing.T) *asynq.Task {
	t.Helper()
	task, err := queue.NewAnalysisTask(uuid.NewString(), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestProcessTaskStoresInsightsAndUsage(t *testing.T) {
	transcript := "李雷：我负责发布说明。"
	store := &fakeStore{claimed: true, attempts: 1}
	extractor := &fakeExtractor{}
	handler := NewHandler(store, fakeRecordings{transcript: &transcript}, extractor, "qwen-test")

	if err := handler.ProcessTask(context.Background(), analysisTask(t)); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	if extractor.got != transcript || store.ready == nil {
		t.Fatalf("extractor got %q ready = %+v", extractor.got, store.ready)
	}
	if store.ready.Model != "qwen-test" || store.ready.PromptTokens != 300 || store.ready.CompletionTokens != 80 ||
		store.ready.LLMCalls != 1 || store.ready.AnalysisMS != 1500 || Validate(store.ready.Result) != nil {
		t.Fatalf("outcome = %+v", store.ready)
	}
}

func TestProcessTaskRetriesTransientLLMErrorsThenFails(t *testing.T) {
	transient := &llm.StatusError{Code: 503, Body: "loading model"}
	store := &fakeStore{claimed: true, attempts: 1}
	handler := NewHandler(store, fakeRecordings{}, &fakeExtractor{err: transient}, "m")
	if err := handler.ProcessTask(context.Background(), analysisTask(t)); err == nil || errors.Is(err, asynq.SkipRetry) || !store.released {
		t.Fatalf("第 1 次应释放租约并交给 asynq 重试：err = %v released = %v", err, store.released)
	}

	store = &fakeStore{claimed: true, attempts: 3}
	handler = NewHandler(store, fakeRecordings{}, &fakeExtractor{err: transient}, "m")
	if err := handler.ProcessTask(context.Background(), analysisTask(t)); !errors.Is(err, asynq.SkipRetry) ||
		!strings.Contains(store.failedMsg, "纪要抽取失败") {
		t.Fatalf("第 3 次应标记 failed：err = %v msg = %q", err, store.failedMsg)
	}
}

func TestProcessTaskFailsImmediatelyOnNonRetryableError(t *testing.T) {
	store := &fakeStore{claimed: true, attempts: 1}
	handler := NewHandler(store, fakeRecordings{}, &fakeExtractor{err: &llm.StatusError{Code: 400, Body: "context overflow"}}, "m")
	if err := handler.ProcessTask(context.Background(), analysisTask(t)); !errors.Is(err, asynq.SkipRetry) || store.failedMsg == "" || store.released {
		t.Fatalf("err = %v store = %+v", err, store)
	}
}

func TestProcessTaskSkipsWhenNotClaimed(t *testing.T) {
	extractor := &fakeExtractor{}
	handler := NewHandler(&fakeStore{}, fakeRecordings{}, extractor, "m")
	if err := handler.ProcessTask(context.Background(), analysisTask(t)); err != nil || extractor.got != "" {
		t.Fatalf("err = %v", err)
	}
	if err := handler.ProcessTask(context.Background(), asynq.NewTask(queue.TypeAnalysis, json.RawMessage(`{`))); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("损坏载荷应 SkipRetry：%v", err)
	}
}
