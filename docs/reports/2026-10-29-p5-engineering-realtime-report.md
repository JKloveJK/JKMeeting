# P5 工程化与轻量实时流报告

- 日期：2026-10-29
- 关联：[P5 需求](../requirements/2026-10-29-p5-engineering-realtime-requirements.md)
- 用户已确认范围：工程化全量 + 轻量实时流（WebSocket + 抖动缓冲）；不做 WebRTC/视频。

## 1. 工程化验收

| 编号 | 结论 | 证据 |
| --- | --- | --- |
| S1 CI | **已落地，未远端验证**：`.github/workflows/ci.yml` 四个 job（core/server/android/apple-bridge）；本地等价门禁 `make p5-ci` 与 core/Go 单测通过。GitHub Actions 首次运行需推送后在远端确认，本轮未推送 | 本地命令输出；见 §3 |
| S2 监控告警 | **通过**：Prometheus（Compose profile `observability`）抓取宿主机 API `/metrics`，target `up`；业务请求后 `jkmeeting_http_requests_total` 可查；`promtool check config` 成功，4 条告警规则已加载 | `ops/prometheus/`；本机 9090 端口实测 |
| S3 不停机迁移 | **通过**：`p5-migration-drill.sh` 在 API 运行中完成 expand（`0006` 新增可空列）+ 回填 8749 行，前后冒烟均通过；contract 删列命令只输出不自动执行 | `server/scripts/p5-migration-drill.sh` 输出 |
| 成本台账 | 已沉淀为指标台账固定口径（`docs/metrics.md` §5 成本行与 P3 报告 §5）：本地推理按 GPU 秒记账，外部单价仅假设换算 | 台账更新 |

注：发现一个既有口径细节——`/healthz` 与 `/metrics` 在 `ServeHTTP` 里于 HTTP 指标埋点注册前返回，故不计入 `jkmeeting_http_requests_total`；业务路由计数正常。本轮只记录不改（避免动 P1 已验证的观测口径）。

## 2. 实时流验收

| 编号 | 结论 | 证据 |
| --- | --- | --- |
| S4 抖动缓冲 | **通过**：`core` 新增纯计算 `JitterBuffer`；30% 确定性丢包（1500 帧丢 440）下输出帧数与时间轴完全一致，440 帧全部静音填充；乱序重排、迟到丢弃、欠载均有单测 | `make test-core`（`jitter_buffer_test`） |
| S5 推流链路 | **通过（本机回环口径）**：`GET /v1/stream`（Bearer）+ `cmd/streamclient` 以 20 ms 节拍推流；5 秒无丢包 received=250/250；30% 注入丢包 sent=174、服务端 lost_estimated=76、流不中断；首帧 `first_frame_ms=0`（同毫秒，回环下远低于 1.5 s 门槛，非公网结论） | `output/p5-stream-smoke.log`、服务端日志 |

实施插曲：WebSocket 需要 `http.Hijacker`，最初被 `statusCapturingWriter` 包装挡住（dial `unexpected EOF`）；已让 `/v1/stream` 在鉴权后绕过包装器直通，并在代码注释中写明原因。

## 3. 已知缺口

- CI 工作流未在 GitHub 远端实际跑过（未推送）；Apple job 用 `macos-14` 跑桥接测试，未含完整 XCTest（耗时与签名约束，留后续优化）。
- 实时流只做到 core + 服务端协议与统计，未做 Apple/Android 客户端推流 UI 与公网弱网验证；“30% 丢包可懂”是抖动缓冲层面的帧连续性证据，不是主观听感结论。
- Prometheus 为单机 profile，未做告警通知渠道（Alertmanager）接入。
