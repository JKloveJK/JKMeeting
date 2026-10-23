package observability

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type queryTraceKey struct{}

type queryStartData struct {
	sql       string
	startedAt time.Time
}

// PGXQueryTracer 实现 pgx.QueryTracer，用于统计 SQL 指标并自动定位慢查询。
type PGXQueryTracer struct {
	SlowThreshold time.Duration
	Metrics       *Metrics
	Logger        *slog.Logger
}

func NewPGXQueryTracer(slowThreshold time.Duration, metrics *Metrics, logger *slog.Logger) *PGXQueryTracer {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGXQueryTracer{
		SlowThreshold: slowThreshold,
		Metrics:       metrics,
		Logger:        logger,
	}
}

func (t *PGXQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTraceKey{}, queryStartData{
		sql:       compactSQL(data.SQL),
		startedAt: time.Now(),
	})
}

func (t *PGXQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryTraceKey{}).(queryStartData)
	if !ok {
		return
	}
	elapsed := time.Since(start.startedAt)
	isSlow := t.SlowThreshold > 0 && elapsed >= t.SlowThreshold
	hasErr := data.Err != nil

	if t.Metrics != nil {
		t.Metrics.ObserveDBQuery(hasErr, isSlow)
	}
	if isSlow {
		tc := FromContext(ctx)
		t.Logger.Warn("检测到 PostgreSQL 慢查询",
			"duration_ms", elapsed.Milliseconds(),
			"threshold_ms", t.SlowThreshold.Milliseconds(),
			"sql", start.sql,
			"trace_id", tc.TraceID,
			"request_id", tc.RequestID,
			"err", data.Err,
		)
	}
}

func compactSQL(sql string) string {
	fields := strings.Fields(sql)
	joined := strings.Join(fields, " ")
	if len(joined) > 160 {
		return joined[:160] + "..."
	}
	return joined
}
