package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type contextKey string

const (
	traceContextKey contextKey = "jkmeeting_trace_context"
)

// TraceContext 保存贯穿 API、DB、Outbox 与 Worker 的 W3C Trace 与 Request ID。
type TraceContext struct {
	TraceID   string
	SpanID    string
	RequestID string
}

// Traceparent 返回符合 W3C Trace Context 规范的 header（00-<trace_id>-<span_id>-01）。
func (tc TraceContext) Traceparent() string {
	traceID := tc.TraceID
	if len(traceID) != 32 {
		traceID = randomHex(16)
	}
	spanID := tc.SpanID
	if len(spanID) != 16 {
		spanID = randomHex(8)
	}
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID)
}

// ParseOrCreate 从请求头 traceparent 与 X-Request-ID 解析或生成新的 TraceContext。
func ParseOrCreate(traceparentHeader, requestIDHeader string) TraceContext {
	traceID := ""
	parts := strings.Split(strings.TrimSpace(traceparentHeader), "-")
	if len(parts) == 4 && parts[0] == "00" && len(parts[1]) == 32 && isHex(parts[1]) && parts[1] != strings.Repeat("0", 32) {
		traceID = strings.ToLower(parts[1])
	}
	if traceID == "" {
		traceID = randomHex(16)
	}
	spanID := randomHex(8)
	requestID := strings.TrimSpace(requestIDHeader)
	if requestID == "" {
		requestID = randomHex(16)
	}
	return TraceContext{
		TraceID:   traceID,
		SpanID:    spanID,
		RequestID: requestID,
	}
}

func WithTrace(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey, tc)
}

func FromContext(ctx context.Context) TraceContext {
	if ctx == nil {
		return TraceContext{}
	}
	if tc, ok := ctx.Value(traceContextKey).(TraceContext); ok {
		return tc
	}
	return TraceContext{}
}

func randomHex(numBytes int) string {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("a", numBytes*2)
	}
	return hex.EncodeToString(buf)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
