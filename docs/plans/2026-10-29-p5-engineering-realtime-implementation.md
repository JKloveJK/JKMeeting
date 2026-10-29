# P5 工程化与轻量实时流技术方案

- 日期：2026-10-29
- 状态：已实施（见 [P5 报告](../reports/2026-10-29-p5-engineering-realtime-report.md)）
- 需求：[P5 工程化与轻量实时流需求](../requirements/2026-10-29-p5-engineering-realtime-requirements.md)

## 1. 工程化

| 项 | 实现 | 说明 |
| --- | --- | --- |
| CI | `.github/workflows/ci.yml`（core / server / android / apple-bridge 四 job） | 版本全部锁定（Wrapper、AGP、Go 1.26）；本地等价入口 `make p5-ci`（core + Go，Android/Apple 由 CI job 与本地专项命令覆盖） |
| 监控 | `ops/prometheus/prometheus.yml` + `alerts.yml`；Compose profile `observability` | API 在宿主机，Prometheus 经 `host.docker.internal:8082` 抓取；仅回环 9090 |
| 成本 | 沿用 P3 token 记账口径写入台账 | 本地推理按 GPU 秒；外部单价只作假设换算并注明 |
| 不停机迁移 | 迁移 `0006`（expand：可空列）+ `p5-migration-drill.sh` | contract 只输出命令、人工确认后执行，脚本不自动删列 |

## 2. 实时流

| 层 | 实现 |
| --- | --- |
| core | `JitterBuffer`（`jitter_buffer.h/.cpp`）：序号重排、丢包静音填充、迟到丢弃、溢出丢最旧；纯单测覆盖 30% 丢包时长一致性 |
| 服务端 | `GET /v1/stream`（Bearer，WebSocket）：二进制帧 `[4B 序号][PCM 320 样本]`，无状态统计（首帧/丢包/乱序），文本 `END` 后回 JSON。注意：该路由须绕过 `statusCapturingWriter`，否则无法 Hijack |
| 客户端验证 | `server/cmd/streamclient`：20 ms 节拍、伪随机丢包注入、读回服务端统计 |

## 3. 验证

- `make test-core`、`make p5-ci`、`make test-mcp`、Prometheus target/rules 实测、推流冒烟 `output/p5-stream-smoke.log`。
