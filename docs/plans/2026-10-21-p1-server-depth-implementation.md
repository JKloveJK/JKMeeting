# P1 服务端纵深技术方案（观测、压测、连接池、百万数据与韧性）

- 日期：2026-10-21
- 需求：`docs/requirements/2026-10-21-p1-server-depth-requirements.md`
- 阶段：P1（服务端纵深 L3 + L4）

## 1. 架构与模块设计

在不改变 P0 四个核心 REST 接口契约的前提下，在 `server/` 内新增或扩展以下模块：

1. **`server/internal/observability/`（新建）**：
   - `trace.go`：解析或生成 W3C `traceparent`（`00-<32hex trace_id>-<16hex span_id>-01`）与 `request_id`，提供 `Context` 存取辅助函数。
   - `metrics.go`：线程安全的轻量级指标注册表（基于 `sync/atomic`），输出标准 Prometheus 文本格式（`GET /metrics`），记录：
     - HTTP 请求总数、按路由与状态码分类计数、延迟直方图（buckets：`1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500 ms`）；
     - 限流拒绝数 `jkmeeting_rate_limited_total`、队列背压拒绝数 `jkmeeting_backpressure_rejected_total`；
     - 数据库查询总数、错误数、慢查询数 `jkmeeting_db_slow_queries_total`；
     - 实时读取 `pgxpool.Stat()` 输出 `acquired_conns`、`idle_conns`、`total_conns`、`max_conns`、`empty_acquire_count`。
   - `pgx_tracer.go`：实现 `pgx.QueryTracer`，在 `TraceQueryEnd` 计算 SQL 耗时，记录指标并在耗时 `>= SlowQueryThreshold` 时打印带 `trace_id`、`request_id`、`sql`、`duration_ms` 的 `WARN` 级别结构化日志。
2. **`server/internal/config/config.go`（扩展）**：
   - 新增可选环境变量（均提供合理默认值，完全兼容现有 `.env`）：
     - `DB_MAX_CONNS`（默认 `25`）、`DB_MIN_CONNS`（默认 `2`）、`DB_ACQUIRE_TIMEOUT`（默认 `3s`）；
     - `SLOW_QUERY_THRESHOLD_MS`（默认 `50`）；
     - `RATE_LIMIT_RPS`（默认 `2000`）、`RATE_LIMIT_BURST`（默认 `2000`）；
     - `MAX_OUTBOX_BACKLOG`（默认 `50000`）、`OUTBOX_BATCH_SIZE`（默认 `64`）。
3. **`server/internal/outbox/repository.go` & `server/internal/queue/outbox_dispatcher.go`（扩展）**：
   - `outbox.Repository` 新增 `CountPending(ctx) (int64, error)`，使用 `idx_transcription_outbox_pending` 索引快速统计 `status IN ('pending', 'dispatching')` 积压量。
   - `queue.Dispatcher` 新增 `DispatchBatch(ctx, limit int) (int, error)` 与后台 `RunBackgroundLoop(ctx, interval, batchSize)`：
     - 单次 `complete` 请求调用有界 `DispatchBatch`；若此时 Redis 短暂不可达，由于 PostgreSQL 事务已将 `recordings.status` 置为 `uploaded` 并写入 `transcription_outbox`，API 记录降级告警日志后仍向客户端返回 `200 OK`，由后台 loop 在 Redis 恢复后自动补投递（体现 Transactional Outbox 的真正解耦价值，避免 Redis 抖动导致客户端上传确认失败）。
     - 当 `DispatchBatch` 按批（如每批 64 条）派发时，即使积压 10 万条任务也不会阻塞单次 HTTP 请求或打爆 Redis。
4. **`server/internal/httpapi/recording_handler.go`（扩展）**：
   - 接入 W3C `traceparent` 与 `X-Request-ID` 响应头及耗时统计；
   - 接入 `golang.org/x/time/rate` 令牌桶限流（超限返回 `429 Too Many Requests`）；
   - 在 `createRecording` 检查 Outbox 背压阈值（`CountPending >= MaxOutboxBacklog` 时返回 `503 Service Unavailable`）；
   - 新增免 Bearer 鉴权的观测与探针端点：`GET /metrics`、`GET /healthz`（业务接口 `/v1/recordings*` 保持严格 Bearer 鉴权）。

## 2. 六项加压实验方案

通过 `server/scripts/p1-experiments.py` 与 `Makefile` 的 `make load` 执行：

1. **实验 1（并发阶梯压测）**：使用并发工作池对 `POST /v1/recordings` 与 `GET /v1/recordings/{id}` 施压（100 → 500 → 1000 并发），采集 P50/P95/P99、RPS 与错误率。
2. **实验 2（`pgxpool` 连接池扫描）**：在固定 200 并发下依次以 `DB_MAX_CONNS = 2, 5, 10, 25, 50, 100` 启动服务并压测，采集吞吐量与 P95 曲线，验证连接池拐点。
3. **实验 3 & 4（100 万行数据量 + `EXPLAIN (ANALYZE, BUFFERS)` 慢查询与分页对比）**：
   - 批量生成 100 万行 `recordings` 数据，执行 `ANALYZE recordings`；
   - 对比：① 无索引过滤（全表扫描 `Seq Scan`）vs ② 复合索引 `(user_id, created_at DESC)` 的 `Index Scan`；③ 深分页 `OFFSET 50000 LIMIT 20` vs ④ 游标分页（Keyset Pagination `WHERE user_id = $1 AND (created_at, id) < ($2, $3) ORDER BY created_at DESC, id DESC LIMIT 20`）的缓冲区命中与实际耗时。
4. **实验 5（故障注入与限流韧性）**：
   - 验证突发流量超过 `RATE_LIMIT_RPS` 时精准触发 `429` 且服务不崩溃；
   - 模拟 Redis 暂停期间调用 `POST /complete`，验证 Outbox 优雅降级返回 `200 OK`，恢复 Redis 后后台自动完成补投递；
   - 模拟 PostgreSQL 不可用时 `/healthz` 返回 `503` 且请求快速失败不挂死连接，恢复后 `/healthz` 自动回到 `200`。
5. **实验 6（10 万级队列堆积与背压）**：
   - 在数据库中注入 10 万条 `pending` Outbox 事件（模拟 Worker 停机期间积压 10 万任务），验证：
     - 当积压量 `>= MAX_OUTBOX_BACKLOG`（50,000）时，新创建请求立即被背压机制以 `503` 拦截；
     - 放开阈值或消费恢复时，`DispatchBatch` 分批平滑投递，API 内存与延迟保持稳定，无雪崩。

## 3. 实施步骤

1. 实现 `server/internal/observability/`（`trace.go`、`metrics.go`、`pgx_tracer.go`）及单测；
2. 扩展 `config`、`outbox`、`queue`、`httpapi` 与 `cmd/api/main.go`、`cmd/worker/main.go`，运行 `make test-server`；
3. 编写 `server/scripts/p1-load.py`（对应 `make load`）与 `server/scripts/p1-experiments.py`，更新 `Makefile`；
4. 运行 `make load` 及全部 P1 实验，生成 `output/p1-*.json` 证据并撰写 `docs/reports/2026-10-21-p1-server-depth-report.md`，更新 `docs/metrics.md` 与 `docs/项目知识复习.md`。
