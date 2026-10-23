# P1 服务端纵深需求（L3 可看 + L4 可扛）

- 日期：2026-10-21
- 状态：已确认
- 关联：[客户端全栈学习路线](../客户端全栈学习路线.md)、[服务端深度路线](../plans/2026-09-29-server-depth-plan.md)、[指标台账](../metrics.md)

## 背景与问题

P0 已打通最小端到端闭环并建立单请求基线，但当前服务端仍停留在 L1/L2（功能正确、基本幂等）：
1. 缺少统一延迟/吞吐/连接池指标暴露与慢查询自动定位，一条请求跨 API、DB、Outbox 与 Worker 尚无标准 Trace 上下文贯通（未达 L3 可看）。
2. `pgxpool` 连接池参数不可配置，无入口限流与队列堆积背压保护；`POST /complete` 同步调用 `DispatchAll()` 在海量积压恢复时会阻塞 HTTP 请求甚至引发雪崩。
3. 尚未执行 100→500→1000 并发压测、`MaxConns` 连接池扫描、100 万行数据量下的 `EXPLAIN (ANALYZE, BUFFERS)` 对比、依赖故障注入及 10 万级队列堆积实验（未达 L4 可扛）。

## 目标

在不增加业务 CRUD 接口的前提下，将服务端推进至 [服务端深度路线](../plans/2026-09-29-server-depth-plan.md) 的 **L3（可看）** 与 **L4（可扛）**，产出可复现压测与实验脚本、真实曲线数据、SQL 执行计划对比、故障恢复证据及配套学习知识沉淀。

## 范围

1. **任务 1（压测基线与连接池调优）**：
   - 支持通过环境变量配置 `pgxpool`（`DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_ACQUIRE_TIMEOUT`）；
   - 落地 `Makefile` 的 `make load` 目标及压测脚本，完成 100 → 500 → 1000 并发压测与 `MaxConns`（2 → 5 → 10 → 25 → 50 → 100）吞吐拐点实验。
2. **任务 2（L3 可观测性建设）**：
   - 支持 W3C `traceparent`（`trace_id` / `span_id`）与 `X-Request-ID` 从 HTTP 入口 → PostgreSQL `pgx.QueryTracer` → Outbox → asynq Worker → Whisper 全链路透传；
   - 实现 `pgx.QueryTracer` 慢查询检测（超过 `SLOW_QUERY_THRESHOLD_MS` 自动输出结构化告警日志并计数）；
   - 新增 `GET /metrics`（标准 Prometheus 文本格式，含请求计数、延迟直方图、限流/背压计数、DB 慢查询与 `pgxpool.Stat()` 实时连接池指标）与 `GET /healthz`（PostgreSQL / Redis / S3 探针）。
3. **任务 3（100 万行数据量与 SQL 执行计划调优）**：
   - 提供 100 万行 `recordings` 数据生成与清理工具；
   - 对比 100 万行规模下全表扫描 / 隐式条件、复合索引 `(user_id, created_at DESC)`、深分页 `OFFSET` 与游标分页（Keyset Pagination）的 `EXPLAIN (ANALYZE, BUFFERS)` 执行计划。
4. **任务 4（故障注入、限流背压与 10 万队列堆积防雪崩）**：
   - 入口令牌桶限流（`RATE_LIMIT_RPS` / `RATE_LIMIT_BURST`，超限返回 `429 Too Many Requests`）；
   - Outbox 积压背压保护（`MAX_OUTBOX_BACKLOG`，超限返回 `503 Service Unavailable`）；
   - 将 `complete` 同步无界 `DispatchAll` 改造为有界批量派发 `DispatchBatch` + 后台定时异步派发器：当 Redis 瞬时不可用时，`complete` 依靠 PostgreSQL Outbox 事务落库即可成功返回（优雅降级），Redis 恢复后自动按批平滑消化 10 万级积压，不引发雪崩。

## 非目标

- 不引入 Kubernetes、微服务拆分、分布式事务或外部商业 APM SaaS。
- 不新增与 P1 纵深无关的业务功能（如多用户登录 UI、LLM 摘要、Android 客户端）。

## 约束与依赖

- 优先复用 Go 标准库与 `server/go.mod` 已有依赖（`pgx/v5`、`golang.org/x/time/rate`、`asynq`），避免引入非必要的重型第三方库。
- 实验结束后必须恢复或清理测试产生的百万级临时数据，保持开发环境干净可复现。

## 验收标准

1. `make test-server` 全部单测与集成测试通过；
2. `GET /metrics` 与 `GET /healthz` 正常工作，日志与响应头可查 `trace_id` 与 `request_id`，慢查询可触发结构化日志与指标增长；
3. `make load` 及 P1 六项实验脚本全部运行通过，原始数据保存于 `output/p1-*.json*`，分析结论写入 `docs/reports/2026-10-21-p1-server-depth-report.md`、`docs/metrics.md` 与 `docs/项目知识复习.md`。

## 待确认问题

无（用户已确认直接落地全部 4 项任务并运行验证）。
