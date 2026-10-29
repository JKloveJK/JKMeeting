package httpapi

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jiankailei/jkmeeting/server/internal/observability"
	"golang.org/x/net/websocket"
)

// 实时推流（P5 轻量版）：客户端按 20 ms 帧推送 PCM，服务端统计首帧延迟、丢包与乱序。
// 帧格式（二进制消息）：[4B 大端序号][S16LE PCM，16 kHz 单声道，320 样本 = 640B]。
// 客户端发文本 "END" 后，服务端回文本 JSON 统计并关闭连接。
// 抖动重排与静音填充由 C++ core 的 JitterBuffer 在播放端承担（口径一致的纯单测覆盖），
// 服务端只做无状态统计，避免为实时链路引入新的有状态组件。

const streamFrameSamples = 320

type streamStats struct {
	Received      int   `json:"received"`
	LostEstimated int   `json:"lost_estimated"`
	Reordered     int   `json:"reordered"`
	Duplicates    int   `json:"duplicates"`
	FirstFrameMS  int64 `json:"first_frame_ms"`
	DurationMS    int64 `json:"duration_ms"`
}

type streamTracker struct {
	startedAt  time.Time
	firstAt    time.Time
	nextSeq    uint32
	started    bool
	seen       map[uint32]struct{}
	stats      streamStats
}

func newStreamTracker(now time.Time) *streamTracker {
	return &streamTracker{startedAt: now, seen: make(map[uint32]struct{})}
}

func (tracker *streamTracker) observe(sequence uint32, now time.Time) {
	if tracker.firstAt.IsZero() {
		tracker.firstAt = now
	}
	if _, ok := tracker.seen[sequence]; ok {
		tracker.stats.Duplicates++
		return
	}
	tracker.seen[sequence] = struct{}{}
	tracker.stats.Received++
	if !tracker.started {
		tracker.started = true
		tracker.nextSeq = sequence + 1
		return
	}
	switch {
	case sequence == tracker.nextSeq:
		tracker.nextSeq++
	case sequence > tracker.nextSeq:
		tracker.stats.LostEstimated += int(sequence - tracker.nextSeq)
		tracker.nextSeq = sequence + 1
	default: // sequence < nextSeq：迟到重排帧
		tracker.stats.Reordered++
		if tracker.stats.LostEstimated > 0 {
			tracker.stats.LostEstimated--
		}
	}
}

func (tracker *streamTracker) finish(now time.Time) streamStats {
	tracker.stats.FirstFrameMS = tracker.firstAt.Sub(tracker.startedAt).Milliseconds()
	if tracker.stats.FirstFrameMS < 0 {
		tracker.stats.FirstFrameMS = 0
	}
	tracker.stats.DurationMS = now.Sub(tracker.startedAt).Milliseconds()
	return tracker.stats
}

func (api *RecordingAPI) stream(response http.ResponseWriter, request *http.Request, tc observability.TraceContext) {
	handler := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		tracker := newStreamTracker(time.Now())
		for {
			var payload []byte
			err := websocket.Message.Receive(conn, &payload)
			if err != nil {
				if err != io.EOF {
					api.logger.Debug("推流连接结束", "request_id", tc.RequestID, "err", err)
				}
				return
			}
			if string(payload) == "END" {
				stats := tracker.finish(time.Now())
				api.logger.Info("推流结束", "request_id", tc.RequestID,
					"received", stats.Received, "lost_estimated", stats.LostEstimated,
					"reordered", stats.Reordered, "first_frame_ms", stats.FirstFrameMS)
				encoded, _ := json.Marshal(stats)
				_ = websocket.Message.Send(conn, string(encoded))
				return
			}
			if len(payload) < 4 {
				continue
			}
			sequence := binary.BigEndian.Uint32(payload[:4])
			tracker.observe(sequence, time.Now())
		}
	})
	handler.ServeHTTP(response, request)
}
