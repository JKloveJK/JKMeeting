package transcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jiankailei/jkmeeting/server/internal/queue"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type fakeJobs struct {
	attempts  int
	claimed   bool
	released  bool
	failedMsg string
	ready     struct {
		masterKey, variants string
		ms                  int
	}
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
func (jobs *fakeJobs) MarkFailed(_ context.Context, _ uuid.UUID, _ uuid.UUID, message string) error {
	jobs.failedMsg = message
	return nil
}
func (jobs *fakeJobs) MarkReady(_ context.Context, _ uuid.UUID, _ uuid.UUID, masterKey, variants string, ms int) error {
	jobs.ready.masterKey, jobs.ready.variants, jobs.ready.ms = masterKey, variants, ms
	return nil
}

type fakeRecordings struct{ record recording.Recording }

func (recordings fakeRecordings) FindByID(context.Context, uuid.UUID) (recording.Recording, error) {
	return recordings.record, nil
}

type fakeObjects struct {
	downloadErr error
	uploaded    map[string]string
}

func (objects *fakeObjects) DownloadToTemp(context.Context, string, int64) (string, error) {
	if objects.downloadErr != nil {
		return "", objects.downloadErr
	}
	file, err := os.CreateTemp("", "fake-input-*.audio")
	if err != nil {
		return "", err
	}
	_ = file.Close()
	return file.Name(), nil
}
func (objects *fakeObjects) UploadFile(_ context.Context, key, _ string, contentType string) error {
	objects.uploaded[key] = contentType
	return nil
}

// fakeRunner 模拟 FFmpeg：写出与真实布局相同的文件，或返回指定错误。
type fakeRunner struct{ err error }

func (runner fakeRunner) Run(_ context.Context, _ string, outDir string, ladder []Variant) error {
	if runner.err != nil {
		return runner.err
	}
	files := []string{"master.m3u8"}
	for i, variant := range ladder {
		files = append(files, variant.Name+"/index.m3u8", variant.Name+"/init_"+string(rune('0'+i))+".mp4", variant.Name+"/seg_000.m4s")
	}
	for _, name := range files {
		path := filepath.Join(outDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func newTask(t *testing.T, id uuid.UUID) *asynq.Task {
	t.Helper()
	task, err := queue.NewTranscodeTask(id.String(), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func setup(runnerErr error, attempts int) (*Handler, *fakeJobs, *fakeObjects, uuid.UUID) {
	id := uuid.New()
	jobs := &fakeJobs{claimed: true, attempts: attempts}
	objects := &fakeObjects{uploaded: map[string]string{}}
	ladder, _ := ParseLadder("32,64")
	handler := NewHandler(jobs, fakeRecordings{recording.Recording{ID: id, ObjectKey: "recordings/x/audio.ogg", DurationMS: 3_000}},
		objects, fakeRunner{err: runnerErr}, ladder)
	return handler, jobs, objects, id
}

func TestProcessTaskUploadsHLSAndMarksReady(t *testing.T) {
	handler, jobs, objects, id := setup(nil, 1)
	if err := handler.ProcessTask(context.Background(), newTask(t, id)); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	prefix := "recordings/" + id.String() + "/hls/"
	if jobs.ready.masterKey != prefix+"master.m3u8" || jobs.ready.variants != "32,64" {
		t.Fatalf("ready = %+v", jobs.ready)
	}
	keys := make([]string, 0, len(objects.uploaded))
	for key := range objects.uploaded {
		keys = append(keys, strings.TrimPrefix(key, prefix))
	}
	sort.Strings(keys)
	want := "32k/index.m3u8 32k/init_0.mp4 32k/seg_000.m4s 64k/index.m3u8 64k/init_1.mp4 64k/seg_000.m4s master.m3u8"
	if strings.Join(keys, " ") != want {
		t.Fatalf("uploaded = %v", keys)
	}
	if objects.uploaded[prefix+"master.m3u8"] != "application/vnd.apple.mpegurl" || objects.uploaded[prefix+"32k/seg_000.m4s"] != "audio/mp4" {
		t.Fatalf("content types = %v", objects.uploaded)
	}
}

func TestProcessTaskFailsWithoutRetryOnDeterministicFFmpegErrors(t *testing.T) {
	for name, runnerErr := range map[string]error{
		"missing binary":  ErrFFmpegNotFound,
		"corrupted input": &FFmpegError{ExitCode: 1, Stderr: "Invalid data found when processing input"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, jobs, objects, id := setup(runnerErr, 1)
			err := handler.ProcessTask(context.Background(), newTask(t, id))
			if !errors.Is(err, asynq.SkipRetry) {
				t.Fatalf("ProcessTask() error = %v, want SkipRetry", err)
			}
			if jobs.failedMsg == "" || jobs.released || len(objects.uploaded) != 0 {
				t.Fatalf("jobs = %+v uploaded = %v", jobs, objects.uploaded)
			}
		})
	}
}

func TestProcessTaskRetriesTransientErrorsThenFails(t *testing.T) {
	handler, jobs, objects, id := setup(nil, 1)
	objects.downloadErr = errors.New("connection reset")
	err := handler.ProcessTask(context.Background(), newTask(t, id))
	if err == nil || errors.Is(err, asynq.SkipRetry) || !jobs.released || jobs.failedMsg != "" {
		t.Fatalf("attempt 1: err = %v jobs = %+v", err, jobs)
	}

	handler, jobs, objects, id = setup(nil, maxAttempts)
	objects.downloadErr = errors.New("connection reset")
	err = handler.ProcessTask(context.Background(), newTask(t, id))
	if !errors.Is(err, asynq.SkipRetry) || !strings.Contains(jobs.failedMsg, "connection reset") {
		t.Fatalf("final attempt: err = %v jobs = %+v", err, jobs)
	}
}

func TestProcessTaskSkipsWhenNotClaimed(t *testing.T) {
	handler, jobs, objects, id := setup(nil, 1)
	jobs.claimed = false
	if err := handler.ProcessTask(context.Background(), newTask(t, id)); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	if len(objects.uploaded) != 0 {
		t.Fatalf("uploaded = %v, want none", objects.uploaded)
	}
}
