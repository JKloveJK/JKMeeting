package observability

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestParseOrCreatePreservesValidTraceparent(t *testing.T) {
	incoming := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc := ParseOrCreate(incoming, "req-123")
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("TraceID = %q, want 4bf92f3577b34da6a3ce929d0e0e4736", tc.TraceID)
	}
	if tc.RequestID != "req-123" {
		t.Fatalf("RequestID = %q, want req-123", tc.RequestID)
	}
	if !strings.HasPrefix(tc.Traceparent(), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("Traceparent() = %q, missing expected trace_id prefix", tc.Traceparent())
	}
}

func TestMetricsRenderPrometheusAndSlowQueryTracer(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetPoolStatsProvider(func() PoolStatsSnapshot {
		return PoolStatsSnapshot{AcquiredConns: 2, IdleConns: 8, TotalConns: 10, MaxConns: 25, EmptyAcquireCount: 3}
	})
	metrics.SetOutboxPendingProvider(func() int64 { return 7 })
	metrics.ObserveHTTPRequest("POST", "/v1/recordings", 201, 12.4)
	metrics.IncRateLimited()
	metrics.IncBackpressureRejected()

	tracer := NewPGXQueryTracer(5*time.Millisecond, metrics, nil)
	ctx := WithTrace(context.Background(), ParseOrCreate("", "req-slow"))
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT pg_sleep(0.01)"})
	time.Sleep(8 * time.Millisecond)
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if metrics.SlowQueryCount() != 1 {
		t.Fatalf("SlowQueryCount() = %d, want 1", metrics.SlowQueryCount())
	}

	rendered := metrics.RenderPrometheus()
	for _, expected := range []string{
		`jkmeeting_http_requests_total{method="POST",route="/v1/recordings",status="201"} 1`,
		`jkmeeting_rate_limited_total 1`,
		`jkmeeting_backpressure_rejected_total 1`,
		`jkmeeting_db_slow_queries_total 1`,
		`jkmeeting_db_pool_max_conns 25`,
		`jkmeeting_outbox_pending_count 7`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("RenderPrometheus() missing %q in:\n%s", expected, rendered)
		}
	}
}
