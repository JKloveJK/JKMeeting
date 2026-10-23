package observability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

var defaultBucketsMs = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500}

// PoolStatsSnapshot 抽象 pgxpool.Stat() 的关键连接池指标，便于单测与 Prometheus 导出。
type PoolStatsSnapshot struct {
	AcquiredConns     int32
	IdleConns         int32
	TotalConns        int32
	MaxConns          int32
	EmptyAcquireCount int64
}

type routeHistogram struct {
	buckets []uint64
	count   uint64
	sumMicros uint64
}

// Metrics 收集 HTTP、限流、背压、数据库慢查询与连接池指标，并以 Prometheus 格式导出。
type Metrics struct {
	mu                   sync.RWMutex
	requestsByKey        map[string]*uint64
	histByRoute          map[string]*routeHistogram
	rateLimitedTotal     uint64
	backpressureTotal    uint64
	dbQueriesTotal       uint64
	dbQueryErrorsTotal   uint64
	dbSlowQueriesTotal   uint64
	poolStatsProvider    func() PoolStatsSnapshot
	outboxPendingFn      func() int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		requestsByKey: make(map[string]*uint64),
		histByRoute:   make(map[string]*routeHistogram),
	}
}

func (m *Metrics) SetPoolStatsProvider(fn func() PoolStatsSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poolStatsProvider = fn
}

func (m *Metrics) SetOutboxPendingProvider(fn func() int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outboxPendingFn = fn
}

func (m *Metrics) ObserveHTTPRequest(method, route string, status int, durationMs float64) {
	key := fmt.Sprintf("%s|%s|%d", method, route, status)
	m.mu.RLock()
	counter := m.requestsByKey[key]
	hist := m.histByRoute[route]
	m.mu.RUnlock()

	if counter == nil || hist == nil {
		m.mu.Lock()
		counter = m.requestsByKey[key]
		if counter == nil {
			var v uint64
			counter = &v
			m.requestsByKey[key] = counter
		}
		hist = m.histByRoute[route]
		if hist == nil {
			hist = &routeHistogram{buckets: make([]uint64, len(defaultBucketsMs))}
			m.histByRoute[route] = hist
		}
		m.mu.Unlock()
	}

	atomic.AddUint64(counter, 1)
	atomic.AddUint64(&hist.count, 1)
	if durationMs > 0 {
		atomic.AddUint64(&hist.sumMicros, uint64(durationMs*1000.0))
	}
	for i, bound := range defaultBucketsMs {
		if durationMs <= bound {
			atomic.AddUint64(&hist.buckets[i], 1)
		}
	}
}

func (m *Metrics) IncRateLimited() {
	atomic.AddUint64(&m.rateLimitedTotal, 1)
}

func (m *Metrics) IncBackpressureRejected() {
	atomic.AddUint64(&m.backpressureTotal, 1)
}

func (m *Metrics) ObserveDBQuery(hasError bool, isSlow bool) {
	atomic.AddUint64(&m.dbQueriesTotal, 1)
	if hasError {
		atomic.AddUint64(&m.dbQueryErrorsTotal, 1)
	}
	if isSlow {
		atomic.AddUint64(&m.dbSlowQueriesTotal, 1)
	}
}

func (m *Metrics) SlowQueryCount() uint64 {
	return atomic.LoadUint64(&m.dbSlowQueriesTotal)
}

func (m *Metrics) RenderPrometheus() string {
	var b strings.Builder

	b.WriteString("# HELP jkmeeting_http_requests_total Total HTTP requests processed.\n")
	b.WriteString("# TYPE jkmeeting_http_requests_total counter\n")

	m.mu.RLock()
	keys := make([]string, 0, len(m.requestsByKey))
	for k := range m.requestsByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		val := atomic.LoadUint64(m.requestsByKey[k])
		fmt.Fprintf(&b, "jkmeeting_http_requests_total{method=%q,route=%q,status=%q} %d\n", parts[0], parts[1], parts[2], val)
	}

	b.WriteString("# HELP jkmeeting_http_request_duration_ms HTTP request latency histogram in milliseconds.\n")
	b.WriteString("# TYPE jkmeeting_http_request_duration_ms histogram\n")
	routes := make([]string, 0, len(m.histByRoute))
	for r := range m.histByRoute {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	for _, r := range routes {
		hist := m.histByRoute[r]
		for i, bound := range defaultBucketsMs {
			cnt := atomic.LoadUint64(&hist.buckets[i])
			fmt.Fprintf(&b, "jkmeeting_http_request_duration_ms_bucket{route=%q,le=\"%g\"} %d\n", r, bound, cnt)
		}
		totalCnt := atomic.LoadUint64(&hist.count)
		sumMs := float64(atomic.LoadUint64(&hist.sumMicros)) / 1000.0
		fmt.Fprintf(&b, "jkmeeting_http_request_duration_ms_bucket{route=%q,le=\"+Inf\"} %d\n", r, totalCnt)
		fmt.Fprintf(&b, "jkmeeting_http_request_duration_ms_sum{route=%q} %.3f\n", r, sumMs)
		fmt.Fprintf(&b, "jkmeeting_http_request_duration_ms_count{route=%q} %d\n", r, totalCnt)
	}
	poolFn := m.poolStatsProvider
	outboxFn := m.outboxPendingFn
	m.mu.RUnlock()

	fmt.Fprintf(&b, "# HELP jkmeeting_rate_limited_total Requests rejected by token bucket rate limiter.\n")
	fmt.Fprintf(&b, "# TYPE jkmeeting_rate_limited_total counter\n")
	fmt.Fprintf(&b, "jkmeeting_rate_limited_total %d\n", atomic.LoadUint64(&m.rateLimitedTotal))

	fmt.Fprintf(&b, "# HELP jkmeeting_backpressure_rejected_total Requests rejected due to outbox queue backlog.\n")
	fmt.Fprintf(&b, "# TYPE jkmeeting_backpressure_rejected_total counter\n")
	fmt.Fprintf(&b, "jkmeeting_backpressure_rejected_total %d\n", atomic.LoadUint64(&m.backpressureTotal))

	fmt.Fprintf(&b, "# HELP jkmeeting_db_queries_total Total PostgreSQL queries traced.\n")
	fmt.Fprintf(&b, "# TYPE jkmeeting_db_queries_total counter\n")
	fmt.Fprintf(&b, "jkmeeting_db_queries_total %d\n", atomic.LoadUint64(&m.dbQueriesTotal))

	fmt.Fprintf(&b, "# HELP jkmeeting_db_query_errors_total Total PostgreSQL query errors.\n")
	fmt.Fprintf(&b, "# TYPE jkmeeting_db_query_errors_total counter\n")
	fmt.Fprintf(&b, "jkmeeting_db_query_errors_total %d\n", atomic.LoadUint64(&m.dbQueryErrorsTotal))

	fmt.Fprintf(&b, "# HELP jkmeeting_db_slow_queries_total Total PostgreSQL queries exceeding slow threshold.\n")
	fmt.Fprintf(&b, "# TYPE jkmeeting_db_slow_queries_total counter\n")
	fmt.Fprintf(&b, "jkmeeting_db_slow_queries_total %d\n", atomic.LoadUint64(&m.dbSlowQueriesTotal))

	if poolFn != nil {
		snap := poolFn()
		fmt.Fprintf(&b, "# HELP jkmeeting_db_pool_acquired_conns Currently acquired connections in pgxpool.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_db_pool_acquired_conns gauge\n")
		fmt.Fprintf(&b, "jkmeeting_db_pool_acquired_conns %d\n", snap.AcquiredConns)

		fmt.Fprintf(&b, "# HELP jkmeeting_db_pool_idle_conns Currently idle connections in pgxpool.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_db_pool_idle_conns gauge\n")
		fmt.Fprintf(&b, "jkmeeting_db_pool_idle_conns %d\n", snap.IdleConns)

		fmt.Fprintf(&b, "# HELP jkmeeting_db_pool_total_conns Currently total connections in pgxpool.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_db_pool_total_conns gauge\n")
		fmt.Fprintf(&b, "jkmeeting_db_pool_total_conns %d\n", snap.TotalConns)

		fmt.Fprintf(&b, "# HELP jkmeeting_db_pool_max_conns Configured MaxConns in pgxpool.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_db_pool_max_conns gauge\n")
		fmt.Fprintf(&b, "jkmeeting_db_pool_max_conns %d\n", snap.MaxConns)

		fmt.Fprintf(&b, "# HELP jkmeeting_db_pool_empty_acquire_total Cumulative waits when pool was exhausted.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_db_pool_empty_acquire_total counter\n")
		fmt.Fprintf(&b, "jkmeeting_db_pool_empty_acquire_total %d\n", snap.EmptyAcquireCount)
	}

	if outboxFn != nil {
		fmt.Fprintf(&b, "# HELP jkmeeting_outbox_pending_count Pending or dispatching outbox events.\n")
		fmt.Fprintf(&b, "# TYPE jkmeeting_outbox_pending_count gauge\n")
		fmt.Fprintf(&b, "jkmeeting_outbox_pending_count %d\n", outboxFn())
	}

	return b.String()
}
