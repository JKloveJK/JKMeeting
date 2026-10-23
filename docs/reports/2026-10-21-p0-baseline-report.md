# P0 端到端闭环性能基线与回收实验报告

- 日期：2026-10-21
- 阶段：P0（最小端到端闭环基线验收）
- 关联文档：[P0 设计](../plans/2026-09-29-p0-minimal-loop-design.md)、[指标台账](../metrics.md)

## 1. 实验环境

- 机器：Apple M1 Pro（32 GB 统一内存），macOS 27.0（Build 26A428）
- 容器与存储底座：Colima（VZ 驱动）+ PostgreSQL 16-alpine + Redis 7-alpine + SeaweedFS 3.68（S3 兼容接口，本机 loopback）
- ASR 引擎：宿主机原生 `whisper-server`（`whisper.cpp` + `ggml-base.bin`，Metal MTL0 Apple M1 Pro GPU 加速）
- 工具链：Xcode 27.1 RC、Go 1.26.1、FFmpeg 8.1

## 2. 输入与测量方法

| 指标 | 输入规格 | 样本量 | 测量边界 |
| --- | --- | --- | --- |
| **core 耗时占比** | 30 秒单声道 48 kHz S16LE PCM（1,024 samples/帧，共 1,407 帧/轮）经 `CoreProcessor` 重采样至 16 kHz 并由 `AppleAudioFileWriter` 写入 `.m4a`（产物 144,639 B） | `N = 20` | 仅累加 `RecordingPipeline.process` 内 `CoreProcessor.resample` 耗时，除以 30,000 ms 采集时长 |
| **上传 P95** | `1,258,291 B`（1.2 MB）`audio/mp4` 载荷，通过 `POST /v1/recordings` 获取预签名 URL 后直传 SeaweedFS S3 | `N = 30` | 单次 HTTP `PUT` 请求开始至收到 `200 OK` 响应的耗时 |
| **ASR RTF** | `say` + `ffmpeg` 合成的 30.0 秒（`30,000 ms`）16 kHz 单声道 AAC `.m4a` 会议英语语音（`259,347 B`） | `N = 10` | Worker 结构化日志 `asr_ms`（Whisper HTTP 转写耗时）与音频时长 `30,000 ms` 的比值 |
| **松手到文本** | 同上 30.0 秒（`30,000 ms`）`.m4a` 语音文件 | `N = 10` | 从发起 `POST /v1/recordings` → `PUT` 上传 → `POST /complete` → 轮询 `GET /v1/recordings/{id}` 拿到 `status=done` 及完整 `transcript` 的总耗时（同时记录服务端 50ms 轮询精确完成时间与客户端 2s 轮询周期感知时间） |

## 3. 复现命令与原始数据路径

1. **C++ core 耗时占比测量**：
   ```sh
   xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS \
     -destination 'platform=macOS,arch=arm64' \
     -only-testing:JKMeeting-macOSTests/RecordingPipelineTests CODE_SIGNING_ALLOWED=NO
   ```
   - 原始输出：终端测试日志（`P0_CORE_SAMPLE` / `P0_CORE_SUMMARY`）

2. **上传 P95、ASR RTF、松手到文本测量**：
   ```sh
   make up-local && make migrate-server
   # 启动 run-whisper / run-worker / run-server 后执行：
   python3 server/scripts/measure-p0-baselines.py
   ```
   - 汇总结果：`output/p0-baseline-summary.json`
   - 上传原始样本（30 条）：`output/p0-upload-baseline.jsonl`
   - 端到端与 ASR 原始样本（10 条）：`output/p0-e2e-baseline.jsonl`
   - Worker 结构化日志：`output/p0-baseline-worker.log`

## 4. 测量数据汇总

| 指标 | 样本量 | 错误率 | P50 | P95 | P99 | 目标/判据 | 结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **上传耗时（1.2 MB PUT）** | 30 | 0% | `15.54 ms` | `25.61 ms` | `32.92 ms` | 建立首次基线 | 已建立 |
| **ASR 单次耗时（30s 音频）** | 10 | 0% | `542.0 ms` | `876.0 ms` | `876.0 ms` | — | 已建立 |
| **ASR 标准 RTF（转写耗时 / 音频时长）** | 10 | 0% | `0.0181` | `0.0292` | `0.0292` | `RTF < 1` | **达标**（远低于 1） |
| **ASR 实时倍速（音频时长 / 转写耗时）** | 10 | 0% | `53.67x` | `59.41x`（P5 为 `34.25x`） | — | `> 1x` | **达标** |
| **松手到文本（服务端精确完成）** | 10 | 0% | `1,472.10 ms`（`1.47 s`） | `1,715.77 ms`（`1.72 s`） | `1,715.77 ms` | `< 15 s` | **达标** |
| **松手到文本（客户端 2s 轮询感知）** | 10 | 0% | `2,020.34 ms`（`2.02 s`） | `2,042.78 ms`（`2.04 s`） | `2,042.78 ms` | `< 15 s` | **达标** |
| **core 处理耗时（30s 48kHz→16kHz）** | 20 | 0% | `31.480 ms` | `66.498 ms` | `186.751 ms` | — | 已建立 |
| **core 耗时占比（core / 30s 采集）** | 20 | 0% | `0.105%` | `0.222%` | `0.623%` | `< 20%` | **达标** |

> 口径说明：`docs/metrics.md` 原表头将 ASR RTF 文字写为“音频时长 / whisper 转写耗时”，而目标列写为 `RTF < 1`。按语音处理标准定义，$\text{RTF} = \text{转写耗时} / \text{音频时长}$（越小越快，`< 1` 代表快于实时）；本报告同时给出标准 RTF（P95 `0.0292 < 1`）与反比实时倍速（P50 `53.67x`），两者均消除歧义。

## 5. 结论与发现

1. **C++ core 实时性充裕**：30 秒 48 kHz 单声道音频重采样至 16 kHz 的 P95 耗时仅 `66.50 ms`，占采集总时长 `0.222%`，不到 20% 预算的百分之一，完全支持后续 P2 引入 Opus 编码及 P5 实时流处理。
2. **Metal 加速下 Whisper base 吞吐极高**：30 秒音频的转写耗时 P50 为 `542 ms`、P95 为 `876 ms`（标准 RTF P95 `0.0292`）。端到端从停止录音触发上传到转写完成，服务端实际耗时 P95 为 `1.72 s`，叠加客户端 2 秒轮询间隔后用户感知 P95 为 `2.04 s`，远优于 `< 15 s` 的 P0 验收判据。
3. **Outbox `dispatching` 崩溃回收已补齐**：`outbox.Repository.ClaimPending` 已支持回收超过 1 分钟仍停留在 `dispatching` 的陈旧事件，且 `queue.Dispatcher` 将 `asynq.ErrTaskIDConflict` / `asynq.ErrDuplicateTask` 视为已幂等入队并推进至 `dispatched`，消除了 P0 异步投递链路的最后一项悬挂状态风险。

## 6. 下一步

- P0 四项核心性能基线已全部建立并留存可复现脚本与原始数据，可正式进入 **P1（服务端纵深）**。
- P1 第一步：在 `Makefile` 中落地 `make load` 压测入口，建立 100 → 500 → 1000 并发下的 API P50/P95/P99 与错误率曲线，并接入 OpenTelemetry + Prometheus 观测。
