# P1 服务端纵深实验报告（L3 可看 + L4 可扛）

- 日期：2026-10-21
- 阶段：P1（服务端纵深）
- 关联文档：[服务端深度路线](../plans/2026-09-29-server-depth-plan.md)、[P1 需求](../requirements/2026-10-21-p1-server-depth-requirements.md)、[P1 实施方案](../plans/2026-10-21-p1-server-depth-implementation.md)、[指标台账](../metrics.md)
- 原始数据：`output/p1-experiments-report.json`、`output/p1-exp1-api.log`、`output/p1-fault-injection-api.log`

---

## 1. 实验环境与复现入口

- **硬件与系统**：Apple M1 Pro（10 核 CPU，32 GB 统一内存），macOS 27.0（Build 26A428）
- **运行拓扑**：Colima（VZ 驱动）+ PostgreSQL 16-alpine（`shm_size: 256mb`）+ Redis 7-alpine + SeaweedFS 3.68 + Go 1.26.1 API
- **一键复现命令**：
  ```sh
  make up-local && make migrate-server
  make p1-experiments   # 运行全部 6 项实验并生成 output/p1-experiments-report.json
  make load             # 运行默认 100 并发 / 2000 请求快速压测基线
  ```

---

## 2. 实验一：100 → 200 → 500 → 1000 并发阶梯压测（动作 #1）

配置：`DB_MAX_CONNS=30`，无速率限制干扰，分别压测读接口 `GET /v1/recordings/{id}` 与写接口 `POST /v1/recordings`。

### 2.1 `GET /v1/recordings/{id}`（主键点查）

| 并发数 (`-c`) | 总请求数 (`-n`) | 吞吐量 (`RPS`) | P50 (`ms`) | P95 (`ms`) | P99 (`ms`) | 错误率 |
| --- | --- | --- | --- | --- | --- | --- |
| `100` | `2,000` | `7,401.71` | `9.41` | `34.48` | `85.69` | `0%` |
| **`200`（吞吐峰值）** | `2,000` | **`8,121.56`** | `23.29` | **`27.75`** | **`30.29`** | `0%` |
| `500` | `2,000` | `4,672.68` | `101.29` | `113.48` | `118.02` | `0%` |
| `1000` | `4,000` | `3,964.49` | `224.30` | `303.66` | `308.63` | `0.50%`（本机临时端口/accept 队列饱和） |

### 2.2 `POST /v1/recordings`（UUID 生成 + PostgreSQL `INSERT` + S3 预签名计算）

| 并发数 (`-c`) | 总请求数 (`-n`) | 吞吐量 (`RPS`) | P50 (`ms`) | P95 (`ms`) | P99 (`ms`) | 错误率 |
| --- | --- | --- | --- | --- | --- | --- |
| `100` | `2,000` | `4,105.88` | `21.32` | `37.77` | `56.94` | `0%` |
| **`200`（吞吐拐点）** | `2,000` | **`4,999.16`** | `37.04` | **`49.28`** | **`51.84`** | `0%` |
| `500` | `2,000` | `4,650.50` | `96.02` | `122.53` | `131.79` | `0%` |
| `1000` | `4,000` | `4,523.99` | `192.77` | `269.21` | `272.40` | `5.20%`（未限流时突发 1000 并发触发 socket backlog 溢出） |

**结论（一个数、一句结论、一次改动）：**
- **拐点位置**：本机单实例吞吐拐点出现在 **`200` 并发**（读 `8,121 RPS`、写 `4,999 RPS`，0 错误）；500 并发时总吞吐不再增长，请求转为在 `pgxpool` 等待队列中排队（排队定律 Little's Law：$L = \lambda W$，吞吐 $\lambda$ 恒定时，并发 $L$ 翻 2.5 倍则延迟 $W$ 同步翻 2.5 倍，P95 从 `49.28 ms` 涨到 `122.53 ms`）。1000 并发裸奔时出现 socket 级丢包，证明**入口必须加令牌桶限流（快速返回 HTTP `429`）**。

---

## 3. 实验二：`pgxpool` 连接池大小扫描（动作 #5）

固定 `200` 并发、`2,000` 次 `POST /v1/recordings` 写请求，扫描 `DB_MAX_CONNS` 从 `2` 到 `80`：

| `DB_MAX_CONNS` | 吞吐量 (`RPS`) | P50 (`ms`) | P95 (`ms`) | P99 (`ms`) | 慢查询数 (`>1ms`) | 现象解读 |
| --- | --- | --- | --- | --- | --- | --- |
| `2` | `1,249.32` | `151.55` | `209.38` | `235.35` | `648` | 连接严重不足，200 个 goroutine 抢 2 条连接，大量时间花在应用层等锁 |
| `5` | `2,242.60` | `87.48` | `104.71` | `106.62` | `1,843` | 吞吐提升 `79%`，P95 减半 |
| `10` | `3,485.46` | `53.05` | **`69.75`** | **`71.33`** | `2,257` | **尾延迟（P95/P99）最优区间**，与宿主机/VM CPU 核心数匹配 |
| **`25`（默认值）** | **`4,067.04`** | **`43.11`** | `92.16` | `104.89` | `3,481` | **总吞吐（RPS）与 P50 达到峰值** |
| `50` | `2,961.55` | `58.13` | `123.59` | `157.93` | `3,859` | 超过拐点，吞吐反而**下降 27%** |
| `80` | `1,956.41` | `72.53` | `200.16` | `524.01` | `3,914` | 连接池过大，吞吐较峰值**暴跌 52%**，P99 恶化至 `524 ms` |

**为什么 `MaxConns` 超过 25 后吞吐反而大幅下降？**
1. PostgreSQL 采用**进程模型**（每个数据库连接对应一个 OS backend 进程）。当活跃并发连接达到 50–80 时，远超 Colima VM 的 CPU 核心数，Linux 内核频繁在几十个 `postgres` 进程间做上下文切换（Context Switch），CPU 缓存命中率下降。
2. 并发 `INSERT` 同时竞争 WAL（Write-Ahead Log）写入锁与 B-Tree 索引页面锁（Buffer Lock），单次 SQL 在 DB 内部执行耗时变长（`>1ms` 慢查询从 `2,257` 激增至 `3,914`）。
3. **工程结论**：`pgxpool` 的 `MaxConns` 绝不是越大越好；经验公式为 $\text{MaxConns} \approx 2 \times \text{CPU Cores}$，本项目中设置在 **`10 ~ 25`** 之间是吞吐与尾延迟的最佳平衡点。

---

## 4. 实验三与四：100 万行数据量与 `EXPLAIN (ANALYZE, BUFFERS)` 慢查询调优（动作 #3 & #4）

向 `recordings` 表批量写入 **1,000,065 行**记录（耗时 `9.73 s`），执行 `ANALYZE recordings` 后在百万行规模下对比四类查询计划：

| 场景 | SQL 特征 | 执行计划节点 | Buffers（8KB 页） | 执行耗时 | 相对差距 |
| --- | --- | --- | --- | --- | --- |
| **A. 无索引列过滤** | `WHERE duration_ms = 150000 ORDER BY created_at DESC LIMIT 20` | `Parallel Seq Scan` + `top-N heapsort`（扫描并过滤掉 332,244 行/worker） | `shared hit=6959 read=12396`（共 **19,355 页 ≈ 151 MB**） | **`45.627 ms`** | 基准（慢 **438x**） |
| **B. 复合索引首页查询** | `WHERE user_id = 'p0-hot-user' ORDER BY created_at DESC LIMIT 20` | `Index Scan using idx_recordings_user_created` | `shared hit=3 read=4`（仅 **7 页 = 56 KB**） | **`0.104 ms`** | **1x（极速）** |
| **C. 复合索引深分页退化** | `WHERE user_id = 'p0-hot-user' ORDER BY created_at DESC OFFSET 50000 LIMIT 20` | 优化器放弃索引，退化为 `Parallel Seq Scan` + `external merge Disk: 2328kB` | `shared hit=7059 read=12296, temp read=270 written=292` | **`65.809 ms`** | 退化 **632x**（且写临时磁盘） |
| **D. 游标分页（Keyset）** | `WHERE user_id = 'p0-hot-user' AND created_at < $cursor ORDER BY created_at DESC LIMIT 20` | `Index Scan using idx_recordings_user_created` | `shared hit=1 read=7`（仅 **8 页 = 64 KB**） | **`0.231 ms`** | 比深分页 `OFFSET` **快 285x** |

**核心洞察：**
1. **为什么有 `(user_id, created_at DESC)` 索引时，`OFFSET 50000` 依然退化成全表扫描 + 磁盘排序（`65.81 ms`）？**
   因为 `OFFSET 50000` 要求数据库先定位并跳过前 50,000 条记录；若走随机 I/O 的回表 Index Scan 访问 5 万个堆页面，代价高于顺序扫全表，因此 PostgreSQL 优化器直接放弃索引改为 `Parallel Seq Scan` 并在磁盘临时文件中做 `external merge`（写盘 `2.3 MB`）。
2. **如何根治深分页退化？**
   改用**游标分页（Keyset / Seek Pagination）**：将上一页最后一条记录的 `(created_at, id)` 作为游标条件 `WHERE user_id = $1 AND created_at < $2 ORDER BY created_at DESC LIMIT 20`，B-Tree 直接通过 $O(\log N)$ 定位到第 50,001 条的位置顺序读 20 条，仅读 **8 个 Buffer 页（64 KB）**，耗时从 `65.81 ms` 降至 **`0.231 ms`**。

---

## 5. 实验五与六：可观测性（L3）、故障注入与 10 万队列堆积防雪崩（动作 #2 & #6）

### 5.1 L3 可观测性验证
- **W3C `traceparent` 与 `X-Request-ID` 贯通**：传入 `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`，响应头返回 `00-4bf92f3577b34da6a3ce929d0e0e4736-c1f4c46000ea18a0-01`（保留同一 `trace_id`，生成当前服务 `span_id`），结构化日志与 `PGXQueryTracer` 慢查询告警均携带同一 `trace_id` 与 `request_id`。
- **Prometheus `/metrics` 实时指标**：直接暴露 `jkmeeting_http_requests_total`、`jkmeeting_http_request_duration_ms_bucket`、`jkmeeting_db_slow_queries_total`、`jkmeeting_db_pool_*` 与 `jkmeeting_outbox_pending_count`。

### 5.2 故障注入与韧性表现（动作 #2）

| 故障场景 | 注入方式 | 服务表现 | 恢复后自愈表现 |
| --- | --- | --- | --- |
| **突发流量超限** | 配置 `RATE_LIMIT_RPS=50, BURST=25`，并发打入 100 请求 | 精准放行 `25` 个请求（`201`），其余 `75` 个立即返回 **`429 Too Many Requests`**（P50 `2.21 ms`），`0` 个 `500` 错误 | 1 秒后令牌桶自动补充，后续请求正常 `201` |
| **Redis 进程冻结 / 宕机** | `docker compose pause redis` 期间调用 `POST /v1/recordings/{id}/complete` | `/healthz` L7 `PING` 探针立即返回 **`503`**（`{"redis":"down","status":"degraded"}`）；但 `POST /complete` 因 PostgreSQL Outbox 事务已持久化，500ms 快速超时后将事件安全回退为 `pending` 并向客户端返回 **`200 OK`（优雅降级，用户无感）** | `docker compose unpause redis` 后，后台 `StartBackgroundLoop` 自动在 `< 1s` 内将该 Outbox 事件补投递至 Redis 并更新为 **`dispatched`**，`/healthz` 自动恢复 `200 OK` |
| **PostgreSQL 暂停** | `docker compose pause postgres` | `/healthz` 立即返回 **`503`**（`{"postgres":"down","status":"degraded"}`） | `unpause` 后连接池自动重连，`/healthz` 立即恢复 `200 OK` |

### 5.3 10 万级队列堆积与背压防雪崩（动作 #6）

- **注入 100,000 条待派发任务**后：
  1. **入口背压保护**：当 `transcription_outbox` 待处理数（`100,000`）达到 `MAX_OUTBOX_BACKLOG`（实验设 `5,000`）时，新发起的 `POST /v1/recordings` 立即返回 **`503 Service Unavailable`**（`{"error_message":"系统转写队列繁忙，触发背压保护，请稍后重试"}`），`/metrics` 中 `jkmeeting_backpressure_rejected_total` 计数加 1，阻止数据库与 Redis 被无限灌爆。
  2. **恢复期防雪崩**：`Dispatcher.DispatchAll` 与后台 `StartBackgroundLoop` 均受 `OUTBOX_BATCH_SIZE=64` 约束，每次仅认领并投递最多 64 条事件，不会在单次 HTTP 请求中同步循环 10 万次导致请求超时或打挂 Redis。
