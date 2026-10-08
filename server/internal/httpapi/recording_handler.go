package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
)

var ErrNotFound = errors.New("recording not found")

type recordingStore interface {
	Create(context.Context, recording.Recording) error
	MarkUploadedAndCreateOutbox(context.Context, uuid.UUID, string) (bool, error)
	FindByID(context.Context, uuid.UUID) (recording.Recording, error)
}

type objectStorage interface {
	PresignedPut(context.Context, string, string, time.Duration) (string, error)
	PresignedGet(context.Context, string, time.Duration) (string, error)
	Exists(context.Context, string) (bool, error)
}

type dispatcher interface {
	DispatchAll(context.Context) error
}

type RecordingAPI struct {
	store      recordingStore
	storage    objectStorage
	dispatcher dispatcher
	token      string
	logger     *slog.Logger
}

type createRecordingRequest struct {
	DurationMS  int    `json:"duration_ms"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

type createRecordingResponse struct {
	RecordingID      string `json:"recording_id"`
	UploadURL        string `json:"upload_url"`
	UploadExpiresAt  string `json:"upload_expires_at"`
	RequestID        string `json:"request_id"`
}

type recordingResponse struct {
	RecordingID  string            `json:"recording_id"`
	Status       recording.Status  `json:"status"`
	DurationMS   int               `json:"duration_ms"`
	Transcript   *string           `json:"transcript"`
	ErrorMessage *string           `json:"error_message"`
}

func NewRecordingAPI(store recordingStore, storage objectStorage, dispatcher dispatcher, token string) *RecordingAPI {
	return &RecordingAPI{store: store, storage: storage, dispatcher: dispatcher, token: token, logger: slog.Default()}
}

func (api *RecordingAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = uuid.NewString()
	}
	response.Header().Set("X-Request-ID", requestID)

	if request.Header.Get("Authorization") != "Bearer "+api.token {
		api.writeError(response, requestID, http.StatusUnauthorized, "未授权")
		return
	}

	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/recordings":
		api.createRecording(response, request, requestID)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/v1/recordings/") && strings.HasSuffix(request.URL.Path, "/complete"):
		api.completeRecording(response, request, requestID)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/recordings/") && strings.HasSuffix(request.URL.Path, "/playback"):
		api.playback(response, request, requestID)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/recordings/"):
		api.getRecording(response, request, requestID)
	default:
		api.writeError(response, requestID, http.StatusNotFound, "接口不存在")
	}
}

func (api *RecordingAPI) createRecording(response http.ResponseWriter, request *http.Request, requestID string) {
	var input createRecordingRequest
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4<<10)).Decode(&input); err != nil {
		api.writeError(response, requestID, http.StatusBadRequest, "请求体无效")
		return
	}
	if input.DurationMS <= 0 || input.SizeBytes < 0 || strings.TrimSpace(input.ContentType) == "" {
		api.writeError(response, requestID, http.StatusBadRequest, "duration_ms、size_bytes 或 content_type 无效")
		return
	}

	id := uuid.New()
	objectKey := "recordings/" + id.String() + "/audio.m4a"
	recordingValue := recording.Recording{
		ID:          id,
		UserID:      "p0-local-user",
		ObjectKey:   objectKey,
		DurationMS:  input.DurationMS,
		SizeBytes:   input.SizeBytes,
		ContentType: input.ContentType,
	}
	if err := api.store.Create(request.Context(), recordingValue); err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "创建录音记录失败")
		return
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	uploadURL, err := api.storage.PresignedPut(request.Context(), objectKey, input.ContentType, 15*time.Minute)
	if err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "生成上传地址失败")
		return
	}
	api.writeJSON(response, http.StatusCreated, createRecordingResponse{id.String(), uploadURL, expiresAt.UTC().Format(time.RFC3339), requestID})
	api.logger.Info("创建录音记录", "request_id", requestID, "recording_id", id)
}

func (api *RecordingAPI) completeRecording(response http.ResponseWriter, request *http.Request, requestID string) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "/complete")
	if !ok {
		api.writeError(response, requestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, requestID, http.StatusNotFound, "录音不存在")
		return
	}
	exists, err := api.storage.Exists(request.Context(), value.ObjectKey)
	if err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "检查上传对象失败")
		return
	}
	if !exists {
		api.writeError(response, requestID, http.StatusConflict, "音频对象尚未上传")
		return
	}
	updated, err := api.store.MarkUploadedAndCreateOutbox(request.Context(), id, requestID)
	if err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "确认上传失败")
		return
	}
	if !updated {
		api.writeError(response, requestID, http.StatusConflict, "录音状态不允许确认上传")
		return
	}
	if err := api.dispatcher.DispatchAll(request.Context()); err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "投递转写任务失败")
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]string{"recording_id": id.String(), "status": string(recording.StatusUploaded)})
	api.logger.Info("确认录音上传", "request_id", requestID, "recording_id", id)
}

func (api *RecordingAPI) getRecording(response http.ResponseWriter, request *http.Request, requestID string) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "")
	if !ok {
		api.writeError(response, requestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, requestID, http.StatusNotFound, "录音不存在")
		return
	}
	api.writeJSON(response, http.StatusOK, recordingResponse{value.ID.String(), value.Status, value.DurationMS, value.Transcript, value.ErrorMessage})
}

func (api *RecordingAPI) playback(response http.ResponseWriter, request *http.Request, requestID string) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "/playback")
	if !ok {
		api.writeError(response, requestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, requestID, http.StatusNotFound, "录音不存在")
		return
	}
	exists, err := api.storage.Exists(request.Context(), value.ObjectKey)
	if err != nil || !exists {
		api.writeError(response, requestID, http.StatusNotFound, "音频对象不存在")
		return
	}
	url, err := api.storage.PresignedGet(request.Context(), value.ObjectKey, 15*time.Minute)
	if err != nil {
		api.writeError(response, requestID, http.StatusInternalServerError, "生成回放地址失败")
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]string{"playback_url": url, "expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)})
}

func (api *RecordingAPI) recordingIDFromPath(path, suffix string) (uuid.UUID, bool) {
	value := strings.TrimPrefix(path, "/v1/recordings/")
	value = strings.TrimSuffix(value, suffix)
	value = strings.Trim(value, "/")
	id, err := uuid.Parse(value)
	return id, err == nil
}

func (api *RecordingAPI) writeError(response http.ResponseWriter, requestID string, status int, message string) {
	api.writeJSON(response, status, map[string]string{"error_message": message, "request_id": requestID})
}

func (api *RecordingAPI) writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}
