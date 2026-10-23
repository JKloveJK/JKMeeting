package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

type fakeRecordingStore struct {
	recording recording.Recording
	updated   bool
}

func (store *fakeRecordingStore) Create(_ context.Context, value recording.Recording) error {
	store.recording = value
	store.recording.Status = recording.StatusCreated
	return nil
}

func (store *fakeRecordingStore) MarkUploadedAndCreateOutbox(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
	store.updated = true
	store.recording.Status = recording.StatusUploaded
	return true, nil
}

func (store *fakeRecordingStore) FindByID(_ context.Context, id uuid.UUID) (recording.Recording, error) {
	if id != store.recording.ID {
		return recording.Recording{}, ErrNotFound
	}
	return store.recording, nil
}

type fakeStorage struct {
	exists bool
}

func (storage *fakeStorage) PresignedPut(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "http://127.0.0.1:9000/recordings/" + key, nil
}
func (storage *fakeStorage) PresignedGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://127.0.0.1:9000/recordings/" + key, nil
}
func (storage *fakeStorage) Exists(_ context.Context, _ string) (bool, error) {
	return storage.exists, nil
}

type fakeDispatcher struct {
	oneCalled bool
	allCalled bool
}

func (dispatcher *fakeDispatcher) DispatchOne(context.Context) error {
	dispatcher.oneCalled = true
	return nil
}
func (dispatcher *fakeDispatcher) DispatchAll(context.Context) error {
	dispatcher.allCalled = true
	return nil
}

func TestCreateRecordingReturnsPresignedUploadURL(t *testing.T) {
	store := &fakeRecordingStore{}
	api := NewRecordingAPI(store, &fakeStorage{}, &fakeDispatcher{}, "fixture-token")
	request := httptest.NewRequest(http.MethodPost, "/v1/recordings", strings.NewReader(`{"duration_ms":1000,"size_bytes":4,"content_type":"audio/mp4"}`))
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()

	api.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var body createRecordingResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if body.RecordingID == "" || body.UploadURL == "" || body.RequestID == "" {
		t.Fatalf("response = %+v, want recording id, upload URL and request id", body)
	}
}

func TestCreateRecordingMapsContentTypeToObjectExtension(t *testing.T) {
	cases := []struct {
		contentType string
		wantStatus  int
		wantSuffix  string
	}{
		{"audio/mp4", http.StatusCreated, "/audio.m4a"},
		{"audio/ogg", http.StatusCreated, "/audio.ogg"},
		{"audio/ogg; codecs=opus", http.StatusBadRequest, ""},
		{"audio/mpeg", http.StatusBadRequest, ""},
		{"audio/mp4-p1-bench", http.StatusBadRequest, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.contentType, func(t *testing.T) {
			store := &fakeRecordingStore{}
			api := NewRecordingAPI(store, &fakeStorage{}, &fakeDispatcher{}, "fixture-token")
			payload, _ := json.Marshal(map[string]any{"duration_ms": 1000, "size_bytes": 4, "content_type": testCase.contentType})
			request := httptest.NewRequest(http.MethodPost, "/v1/recordings", strings.NewReader(string(payload)))
			request.Header.Set("Authorization", "Bearer fixture-token")
			response := httptest.NewRecorder()

			api.ServeHTTP(response, request)

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.wantStatus, response.Body.String())
			}
			if testCase.wantStatus != http.StatusCreated {
				if store.recording.ID != uuid.Nil {
					t.Fatalf("rejected content type must not create a record: %+v", store.recording)
				}
				return
			}
			var body createRecordingResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response error = %v", err)
			}
			if !strings.HasSuffix(store.recording.ObjectKey, testCase.wantSuffix) ||
				!strings.HasSuffix(body.UploadURL, testCase.wantSuffix) {
				t.Fatalf("object key = %q, upload url = %q, want suffix %q", store.recording.ObjectKey, body.UploadURL, testCase.wantSuffix)
			}
			if store.recording.ContentType != testCase.contentType {
				t.Fatalf("content type = %q, want %q", store.recording.ContentType, testCase.contentType)
			}
		})
	}
}

func TestGetRecordingAndPlaybackURL(t *testing.T) {
	id := uuid.New()
	store := &fakeRecordingStore{recording: recording.Recording{ID: id, ObjectKey: "recordings/x/audio.m4a", Status: recording.StatusUploaded, DurationMS: 1_000}}
	api := NewRecordingAPI(store, &fakeStorage{exists: true}, &fakeDispatcher{}, "fixture-token")

	getRequest := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String(), nil)
	getRequest.Header.Set("Authorization", "Bearer fixture-token")
	getResponse := httptest.NewRecorder()
	api.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getResponse.Code, http.StatusOK)
	}

	playbackRequest := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String()+"/playback", nil)
	playbackRequest.Header.Set("Authorization", "Bearer fixture-token")
	playbackResponse := httptest.NewRecorder()
	api.ServeHTTP(playbackResponse, playbackRequest)
	if playbackResponse.Code != http.StatusOK {
		t.Fatalf("playback status = %d, want %d", playbackResponse.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.NewDecoder(playbackResponse.Body).Decode(&body); err != nil {
		t.Fatalf("decode playback response error = %v", err)
	}
	if body["playback_url"] == "" {
		t.Fatal("playback_url is empty")
	}
}

func TestCompleteDrainsAllPendingOutboxEvents(t *testing.T) {
	id := uuid.New()
	store := &fakeRecordingStore{recording: recording.Recording{ID: id, ObjectKey: "recordings/x/audio.m4a", Status: recording.StatusCreated}}
	dispatcher := &fakeDispatcher{}
	api := NewRecordingAPI(store, &fakeStorage{exists: true}, dispatcher, "fixture-token")
	request := httptest.NewRequest(http.MethodPost, "/v1/recordings/"+id.String()+"/complete", nil)
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()

	api.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !dispatcher.allCalled {
		t.Fatal("complete must call DispatchAll so older pending events cannot block this recording")
	}
	if dispatcher.oneCalled {
		t.Fatal("complete must not dispatch only one outbox event")
	}
}

func TestCompleteRejectsMissingObject(t *testing.T) {
	id := uuid.New()
	store := &fakeRecordingStore{recording: recording.Recording{ID: id, ObjectKey: "recordings/x/audio.m4a", Status: recording.StatusCreated}}
	api := NewRecordingAPI(store, &fakeStorage{exists: false}, &fakeDispatcher{}, "fixture-token")
	request := httptest.NewRequest(http.MethodPost, "/v1/recordings/"+id.String()+"/complete", nil)
	request.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()

	api.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

type failingDispatcher struct{}

func (failingDispatcher) DispatchAll(context.Context) error {
	return ErrNotFound
}

func TestRateLimitAndBackpressureAndMetricsAndGracefulOutbox(t *testing.T) {
	id := uuid.New()
	store := &fakeRecordingStore{recording: recording.Recording{ID: id, ObjectKey: "recordings/x/audio.m4a", Status: recording.StatusCreated}}

	// 1. 测试限流 429
	limitedAPI := NewRecordingAPI(store, &fakeStorage{}, &fakeDispatcher{}, "fixture-token").WithRateLimit(1, 1)
	req1 := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String(), nil)
	req1.Header.Set("Authorization", "Bearer fixture-token")
	req1.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp1 := httptest.NewRecorder()
	limitedAPI.ServeHTTP(resp1, req1)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.Code)
	}
	if !strings.HasPrefix(resp1.Header().Get("traceparent"), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("traceparent header = %q, want preserved trace_id", resp1.Header().Get("traceparent"))
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/recordings/"+id.String(), nil)
	req2.Header.Set("Authorization", "Bearer fixture-token")
	resp2 := httptest.NewRecorder()
	limitedAPI.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", resp2.Code)
	}

	// 2. 测试背压 503
	bpAPI := NewRecordingAPI(store, &fakeStorage{}, &fakeDispatcher{}, "fixture-token").
		WithBackpressure(func(context.Context) (int64, error) { return 100, nil }, 50)
	createReq := httptest.NewRequest(http.MethodPost, "/v1/recordings", strings.NewReader(`{"duration_ms":1000,"size_bytes":4,"content_type":"audio/mp4"}`))
	createReq.Header.Set("Authorization", "Bearer fixture-token")
	createResp := httptest.NewRecorder()
	bpAPI.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("backpressure status = %d, want 503", createResp.Code)
	}

	// 3. 测试 Redis 故障时 Outbox 优雅降级 200 OK
	gracefulAPI := NewRecordingAPI(store, &fakeStorage{exists: true}, failingDispatcher{}, "fixture-token").
		WithGracefulOutboxFallback(true)
	compReq := httptest.NewRequest(http.MethodPost, "/v1/recordings/"+id.String()+"/complete", nil)
	compReq.Header.Set("Authorization", "Bearer fixture-token")
	compResp := httptest.NewRecorder()
	gracefulAPI.ServeHTTP(compResp, compReq)
	if compResp.Code != http.StatusOK {
		t.Fatalf("graceful complete status = %d, want 200: %s", compResp.Code, compResp.Body.String())
	}

	// 4. 测试 /metrics 输出
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResp := httptest.NewRecorder()
	limitedAPI.ServeHTTP(metricsResp, metricsReq)
	if metricsResp.Code != http.StatusOK || !strings.Contains(metricsResp.Body.String(), "jkmeeting_rate_limited_total 1") {
		t.Fatalf("/metrics unexpected response: %d\n%s", metricsResp.Code, metricsResp.Body.String())
	}
}
