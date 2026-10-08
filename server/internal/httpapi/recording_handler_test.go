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
