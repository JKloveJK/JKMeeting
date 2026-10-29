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
	"github.com/jiankailei/jkmeeting/server/internal/observability"
	"github.com/jiankailei/jkmeeting/server/internal/recording"
	"golang.org/x/time/rate"
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
	store                 recordingStore
	storage               objectStorage
	dispatcher            dispatcher
	token                 string
	logger                *slog.Logger
	metrics               *observability.Metrics
	limiter               *rate.Limiter
	backlogCounter        func(context.Context) (int64, error)
	maxOutboxBacklog      int64
	healthCheckFn         func(context.Context) map[string]string
	gracefulOutboxDegrade bool
	transcodes            transcodeStore
	objectReader          objectReader
	hlsSigner             *HLSSigner
	insights              insightsStore
	asker                 asker
	defaultTopK           int
}

type createRecordingRequest struct {
	DurationMS  int    `json:"duration_ms"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

// 精确匹配：预签名 PUT 会把 Content-Type 纳入签名，客户端上传时须使用完全相同的值。
var audioObjectExtensions = map[string]string{
	"audio/mp4": "m4a",
	"audio/ogg": "ogg",
}

type createRecordingResponse struct {
	RecordingID     string `json:"recording_id"`
	UploadURL       string `json:"upload_url"`
	UploadExpiresAt string `json:"upload_expires_at"`
	RequestID       string `json:"request_id"`
}

type recordingResponse struct {
	RecordingID  string           `json:"recording_id"`
	Status       recording.Status `json:"status"`
	DurationMS   int              `json:"duration_ms"`
	Transcript   *string          `json:"transcript"`
	ErrorMessage *string          `json:"error_message"`
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func NewRecordingAPI(store recordingStore, storage objectStorage, dispatcher dispatcher, token string) *RecordingAPI {
	return &RecordingAPI{
		store:      store,
		storage:    storage,
		dispatcher: dispatcher,
		token:      token,
		logger:     slog.Default(),
		metrics:    observability.NewMetrics(),
	}
}

func (api *RecordingAPI) WithMetrics(metrics *observability.Metrics) *RecordingAPI {
	if metrics != nil {
		api.metrics = metrics
	}
	return api
}

func (api *RecordingAPI) WithRateLimit(rps float64, burst int) *RecordingAPI {
	if rps > 0 && burst > 0 {
		api.limiter = rate.NewLimiter(rate.Limit(rps), burst)
	}
	return api
}

func (api *RecordingAPI) WithBackpressure(counter func(context.Context) (int64, error), maxBacklog int64) *RecordingAPI {
	api.backlogCounter = counter
	api.maxOutboxBacklog = maxBacklog
	return api
}

func (api *RecordingAPI) WithHealthCheck(fn func(context.Context) map[string]string) *RecordingAPI {
	api.healthCheckFn = fn
	return api
}

func (api *RecordingAPI) WithGracefulOutboxFallback(enabled bool) *RecordingAPI {
	api.gracefulOutboxDegrade = enabled
	return api
}

func (api *RecordingAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	started := time.Now()
	tc := observability.ParseOrCreate(request.Header.Get("traceparent"), request.Header.Get("X-Request-ID"))
	request = request.WithContext(observability.WithTrace(request.Context(), tc))

	response.Header().Set("X-Request-ID", tc.RequestID)
	response.Header().Set("traceparent", tc.Traceparent())

	// 免鉴权观测与健康探针端点
	if request.Method == http.MethodGet && request.URL.Path == "/metrics" {
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(api.metrics.RenderPrometheus()))
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		api.serveHealthz(response, request)
		return
	}

	// WebSocket 推流需要 Hijack 原始连接，不能走 statusCapturingWriter 包装；单独在鉴权后直通。
	if request.Method == http.MethodGet && request.URL.Path == "/v1/stream" {
		if request.Header.Get("Authorization") != "Bearer "+api.token {
			api.writeError(response, tc.RequestID, http.StatusUnauthorized, "未授权")
			return
		}
		api.stream(response, request, tc)
		return
	}

	route := classifyRoute(request.Method, request.URL.Path)
	writer := &statusCapturingWriter{ResponseWriter: response, status: http.StatusOK}
	defer func() {
		elapsedMs := float64(time.Since(started).Microseconds()) / 1000.0
		api.metrics.ObserveHTTPRequest(request.Method, route, writer.status, elapsedMs)
	}()

	// HLS playlist 由 AVPlayer 直接请求，以 URL 签名代替 Bearer。
	if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/hls/") && api.transcodes != nil {
		if !api.rateLimited(writer, tc) {
			api.serveHLS(writer, request, tc)
		}
		return
	}

	if request.Header.Get("Authorization") != "Bearer "+api.token {
		api.writeError(writer, tc.RequestID, http.StatusUnauthorized, "未授权")
		return
	}

	if api.rateLimited(writer, tc) {
		return
	}

	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/recordings":
		api.createRecording(writer, request, tc)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/ask":
		api.ask(writer, request, tc)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/v1/recordings/") && strings.HasSuffix(request.URL.Path, "/complete"):
		api.completeRecording(writer, request, tc)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/recordings/") && strings.HasSuffix(request.URL.Path, "/playback"):
		api.playback(writer, request, tc)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/recordings/") && strings.HasSuffix(request.URL.Path, "/insights"):
		api.getInsights(writer, request, tc)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/recordings/"):
		api.getRecording(writer, request, tc)
	default:
		api.writeError(writer, tc.RequestID, http.StatusNotFound, "接口不存在")
	}
}

func (api *RecordingAPI) serveHealthz(response http.ResponseWriter, request *http.Request) {
	statusMap := map[string]string{"status": "ok"}
	httpStatus := http.StatusOK
	if api.healthCheckFn != nil {
		components := api.healthCheckFn(request.Context())
		for k, v := range components {
			statusMap[k] = v
			if v != "ok" {
				statusMap["status"] = "degraded"
				httpStatus = http.StatusServiceUnavailable
			}
		}
	}
	api.writeJSON(response, httpStatus, statusMap)
}

func (api *RecordingAPI) createRecording(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	if api.backlogCounter != nil && api.maxOutboxBacklog > 0 {
		backlog, err := api.backlogCounter(request.Context())
		if err == nil && backlog >= api.maxOutboxBacklog {
			api.metrics.IncBackpressureRejected()
			response.Header().Set("Retry-After", "5")
			api.writeError(response, tc.RequestID, http.StatusServiceUnavailable, "系统转写队列繁忙，触发背压保护，请稍后重试")
			return
		}
	}

	var input createRecordingRequest
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4<<10)).Decode(&input); err != nil {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "请求体无效")
		return
	}
	extension, supported := audioObjectExtensions[input.ContentType]
	if input.DurationMS <= 0 || input.SizeBytes < 0 || !supported {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "duration_ms、size_bytes 或 content_type 无效（content_type 仅支持 audio/mp4、audio/ogg）")
		return
	}

	id := uuid.New()
	objectKey := "recordings/" + id.String() + "/audio." + extension
	recordingValue := recording.Recording{
		ID:          id,
		UserID:      "p0-local-user",
		ObjectKey:   objectKey,
		DurationMS:  input.DurationMS,
		SizeBytes:   input.SizeBytes,
		ContentType: input.ContentType,
	}
	if err := api.store.Create(request.Context(), recordingValue); err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "创建录音记录失败")
		return
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	uploadURL, err := api.storage.PresignedPut(request.Context(), objectKey, input.ContentType, 15*time.Minute)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "生成上传地址失败")
		return
	}
	api.writeJSON(response, http.StatusCreated, createRecordingResponse{id.String(), uploadURL, expiresAt.UTC().Format(time.RFC3339), tc.RequestID})
	api.logger.Info("创建录音记录", "request_id", tc.RequestID, "trace_id", tc.TraceID, "recording_id", id)
}

func (api *RecordingAPI) completeRecording(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "/complete")
	if !ok {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "录音不存在")
		return
	}
	exists, err := api.storage.Exists(request.Context(), value.ObjectKey)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "检查上传对象失败")
		return
	}
	if !exists {
		api.writeError(response, tc.RequestID, http.StatusConflict, "音频对象尚未上传")
		return
	}
	updated, err := api.store.MarkUploadedAndCreateOutbox(request.Context(), id, tc.RequestID)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "确认上传失败")
		return
	}
	if !updated {
		api.writeError(response, tc.RequestID, http.StatusConflict, "录音状态不允许确认上传")
		return
	}
	dispatchCtx, cancelDispatch := context.WithTimeout(request.Context(), 800*time.Millisecond)
	defer cancelDispatch()
	if err := api.dispatcher.DispatchAll(dispatchCtx); err != nil {
		if api.gracefulOutboxDegrade {
			api.logger.Warn("同步派发 Outbox 遇到临时故障，已由事务 Outbox 持久化等待后台自愈",
				"request_id", tc.RequestID,
				"trace_id", tc.TraceID,
				"recording_id", id,
				"err", err,
			)
		} else {
			api.writeError(response, tc.RequestID, http.StatusInternalServerError, "投递转写任务失败")
			return
		}
	}
	api.writeJSON(response, http.StatusOK, map[string]string{"recording_id": id.String(), "status": string(recording.StatusUploaded)})
	api.logger.Info("确认录音上传", "request_id", tc.RequestID, "trace_id", tc.TraceID, "recording_id", id)
}

func (api *RecordingAPI) getRecording(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "")
	if !ok {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "录音不存在")
		return
	}
	api.writeJSON(response, http.StatusOK, recordingResponse{value.ID.String(), value.Status, value.DurationMS, value.Transcript, value.ErrorMessage})
}

func (api *RecordingAPI) playback(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	id, ok := api.recordingIDFromPath(request.URL.Path, "/playback")
	if !ok {
		api.writeError(response, tc.RequestID, http.StatusBadRequest, "录音 ID 无效")
		return
	}
	value, err := api.store.FindByID(request.Context(), id)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "录音不存在")
		return
	}
	exists, err := api.storage.Exists(request.Context(), value.ObjectKey)
	if err != nil || !exists {
		api.writeError(response, tc.RequestID, http.StatusNotFound, "音频对象不存在")
		return
	}
	url, err := api.storage.PresignedGet(request.Context(), value.ObjectKey, 15*time.Minute)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "生成回放地址失败")
		return
	}
	body := map[string]string{"playback_url": url, "expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)}
	hlsFields, err := api.playbackHLSFields(request.Context(), id)
	if err != nil {
		api.writeError(response, tc.RequestID, http.StatusInternalServerError, "查询转码状态失败")
		return
	}
	for key, value := range hlsFields {
		body[key] = value
	}
	api.writeJSON(response, http.StatusOK, body)
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

func (api *RecordingAPI) rateLimited(writer http.ResponseWriter, tc observability.TraceContext) bool {
	if api.limiter == nil || api.limiter.Allow() {
		return false
	}
	api.metrics.IncRateLimited()
	writer.Header().Set("Retry-After", "1")
	api.writeError(writer, tc.RequestID, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
	return true
}

func (api *RecordingAPI) writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func classifyRoute(method, path string) string {
	switch {
	case method == http.MethodPost && path == "/v1/recordings":
		return "POST /v1/recordings"
	case method == http.MethodPost && path == "/v1/ask":
		return "POST /v1/ask"
	case method == http.MethodPost && strings.HasPrefix(path, "/v1/recordings/") && strings.HasSuffix(path, "/complete"):
		return "POST /v1/recordings/{id}/complete"
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/recordings/") && strings.HasSuffix(path, "/playback"):
		return "GET /v1/recordings/{id}/playback"
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/recordings/") && strings.HasSuffix(path, "/insights"):
		return "GET /v1/recordings/{id}/insights"
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/recordings/"):
		return "GET /v1/recordings/{id}"
	case method == http.MethodGet && strings.HasPrefix(path, "/v1/hls/"):
		return "GET /v1/hls/{id}/{playlist}"
	default:
		return method + " /unknown"
	}
}
