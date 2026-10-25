package rag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/embedding"
	"github.com/jiankailei/jkmeeting/server/internal/llm"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type fakeEmbedder struct {
	err   error
	calls int
}

func (embedder *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, int, error) {
	embedder.calls++
	if embedder.err != nil {
		return nil, 0, embedder.err
	}
	vectors := make([][]float32, len(inputs))
	for i := range inputs {
		vectors[i] = []float32{1, 0}
	}
	return vectors, len(inputs), nil
}

type fakeSearch struct{ hits []Hit }

func (search fakeSearch) Search(context.Context, []float32, int) ([]Hit, error) {
	return search.hits, nil
}

type fakeChat struct {
	reply  string
	err    error
	prompt string
}

func (chat *fakeChat) ChatJSON(_ context.Context, messages []llm.Message, _ json.RawMessage, validate func([]byte) error) (llm.Result, error) {
	chat.prompt = messages[len(messages)-1].Content
	if chat.err != nil {
		return llm.Result{}, chat.err
	}
	if err := validate([]byte(chat.reply)); err != nil {
		return llm.Result{}, err
	}
	return llm.Result{Content: json.RawMessage(chat.reply), Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
}

func threeHits() []Hit {
	return []Hit{
		{ChunkID: 1, RecordingID: uuid.New(), Content: "下周三发布 v2.1", Distance: 0.2},
		{ChunkID: 2, RecordingID: uuid.New(), Content: "连接池提高到两百", Distance: 0.4},
		{ChunkID: 3, RecordingID: uuid.New(), Content: "团建去徒步", Distance: 0.6},
	}
}

func TestAskKeepsOnlyRetrievedCitations(t *testing.T) {
	chat := &fakeChat{reply: `{"answer":"下周三发布","citations":[1,1,7,0],"insufficient":false}`}
	answer, err := NewAnswerer(&fakeEmbedder{}, fakeSearch{hits: threeHits()}, chat).Ask(context.Background(), "什么时候发布？", 3)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Insufficient || len(answer.Citations) != 1 || answer.Citations[0].ChunkID != 1 || answer.InvalidCitations != 2 {
		t.Fatalf("answer = %+v", answer)
	}
	if !strings.Contains(chat.prompt, "[1]") || !strings.Contains(chat.prompt, "[3]") || !strings.Contains(chat.prompt, "什么时候发布？") {
		t.Fatalf("prompt = %q", chat.prompt)
	}
}

func TestAskTreatsUncitedAnswerAsInsufficient(t *testing.T) {
	chat := &fakeChat{reply: `{"answer":"编造的答案","citations":[9],"insufficient":false}`}
	answer, err := NewAnswerer(&fakeEmbedder{}, fakeSearch{hits: threeHits()}, chat).Ask(context.Background(), "q", 3)
	if err != nil || !answer.Insufficient || answer.Text != "资料不足" || answer.Citations != nil {
		t.Fatalf("answer = %+v err = %v", answer, err)
	}
}

func TestAskSkipsLLMWhenIndexEmptyAndWrapsEmbeddingErrors(t *testing.T) {
	chat := &fakeChat{}
	answer, err := NewAnswerer(&fakeEmbedder{}, fakeSearch{}, chat).Ask(context.Background(), "q", 5)
	if err != nil || !answer.Insufficient || answer.LLMCalled || chat.prompt != "" {
		t.Fatalf("answer = %+v err = %v", answer, err)
	}
	_, err = NewAnswerer(&fakeEmbedder{err: errors.New("down")}, fakeSearch{}, chat).Ask(context.Background(), "q", 5)
	if !errors.Is(err, ErrEmbedding) {
		t.Fatalf("err = %v", err)
	}
}

type fakeJobs struct {
	claimed   bool
	attempts  int
	released  bool
	failedMsg string
	chunks    []Chunk
	vectors   [][]float32
	ready     bool
}

func (jobs *fakeJobs) Claim(context.Context, uuid.UUID, time.Duration) (Claim, error) {
	if !jobs.claimed {
		return Claim{}, nil
	}
	return Claim{Claimed: true, LeaseToken: uuid.New(), Attempts: jobs.attempts}, nil
}
func (jobs *fakeJobs) ReleaseForRetry(context.Context, uuid.UUID, uuid.UUID) error {
	jobs.released = true
	return nil
}
func (jobs *fakeJobs) MarkFailed(_ context.Context, _, _ uuid.UUID, message string) error {
	jobs.failedMsg = message
	return nil
}
func (jobs *fakeJobs) MarkReadyWithChunks(_ context.Context, _, _ uuid.UUID, chunks []Chunk, vectors [][]float32, _ int) error {
	jobs.ready, jobs.chunks, jobs.vectors = true, chunks, vectors
	return nil
}

type fakeRecordings struct{ transcript *string }

func (recordings fakeRecordings) FindByID(context.Context, uuid.UUID) (recording.Recording, error) {
	return recording.Recording{Transcript: recordings.transcript}, nil
}

func indexTask(t *testing.T) *asynq.Task {
	t.Helper()
	task, err := queue.NewIndexTask(uuid.NewString(), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestIndexerStoresChunksAndHandlesFailures(t *testing.T) {
	transcript := "李雷：下周三发布。韩梅梅：先灰度。"
	jobs := &fakeJobs{claimed: true, attempts: 1}
	if err := NewIndexer(jobs, fakeRecordings{transcript: &transcript}, &fakeEmbedder{}).ProcessTask(context.Background(), indexTask(t)); err != nil {
		t.Fatal(err)
	}
	if !jobs.ready || len(jobs.chunks) != 1 || len(jobs.vectors) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}

	empty := &fakeJobs{claimed: true, attempts: 1}
	embedder := &fakeEmbedder{}
	if err := NewIndexer(empty, fakeRecordings{}, embedder).ProcessTask(context.Background(), indexTask(t)); err != nil || !empty.ready || embedder.calls != 0 {
		t.Fatalf("空转写应直接 ready 且不调用 embedding：%+v calls = %d", empty, embedder.calls)
	}

	transient := &fakeJobs{claimed: true, attempts: 1}
	err := NewIndexer(transient, fakeRecordings{transcript: &transcript}, &fakeEmbedder{err: &llm.StatusError{Code: 503}}).ProcessTask(context.Background(), indexTask(t))
	if err == nil || errors.Is(err, asynq.SkipRetry) || !transient.released {
		t.Fatalf("临时失败应释放租约重试：err = %v jobs = %+v", err, transient)
	}

	misconfigured := &fakeJobs{claimed: true, attempts: 1}
	err = NewIndexer(misconfigured, fakeRecordings{transcript: &transcript}, &fakeEmbedder{err: embedding.ErrDimension}).ProcessTask(context.Background(), indexTask(t))
	if !errors.Is(err, asynq.SkipRetry) || misconfigured.failedMsg == "" {
		t.Fatalf("维度错误应直接 failed：err = %v jobs = %+v", err, misconfigured)
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := VectorLiteral([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Fatalf("got %q", got)
	}
}
