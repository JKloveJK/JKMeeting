# JKMeeting 指标台账

> 指标用于验证学习结论与优化收益，不用于“看起来很快”。每次记录必须包含环境、输入、命令、原始结果路径和结论；没有基线不能宣称优化。

## 1. 记录规则

- 每个指标记录 P50 / P95 / P99（适用时）、样本量、错误率与测量日期。
- 性能比较必须保持同一输入、同一版本、同一环境；条件变化时新开一行，不覆盖历史值。
- 原始日志、压测输出、trace 或 SQL 计划保留在对应实验报告中，路径填入“证据”列。
- 结论只描述数据实际支持的范围；若存在混杂因素，标为“待复测”。

## 2. P0 基线（端到端闭环）

| 指标 | 定义 | 目标/判据 | 当前值 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 上传 P95 | 录音文件 PUT 预签名 URL 的耗时分布 | 首次建立基线；1.2 MB、本机/局域网场景 | P50 `15.54 ms` / P95 `25.61 ms` / P99 `32.92 ms`（`N=30`，错误率 `0%`） | 2026-10-21，Apple M1 Pro（32 GB）、macOS 27.0、Colima + SeaweedFS 3.68 loopback；`1,258,291 B`（1.2 MB）`audio/mp4` | `python3 server/scripts/measure-p0-baselines.py`；原始数据 `output/p0-upload-baseline.jsonl`；报告 [2026-10-21-p0-baseline-report.md](reports/2026-10-21-p0-baseline-report.md) | 已完成（本机 loopback 基线；真机局域网待实机补测） |
| ASR RTF | 标准 RTF = 转写耗时 / 音频时长（附音频时长 / 转写耗时实时倍速） | `RTF < 1` | 标准 RTF：P50 `0.0181` / P95 `0.0292` / P99 `0.0292`；实时倍速：P50 `53.67x`（单次转写 P50 `542 ms` / P95 `876 ms`，`N=10`，错误率 `0%`） | 2026-10-21，Apple M1 Pro Metal（`whisper.cpp` `ggml-base.bin`）；30.0 秒（`30,000 ms`，`259,347 B`）16 kHz 单声道 AAC `.m4a` | `python3 server/scripts/measure-p0-baselines.py`；原始数据 `output/p0-e2e-baseline.jsonl`、`output/p0-baseline-worker.log`；报告 [2026-10-21-p0-baseline-report.md](reports/2026-10-21-p0-baseline-report.md) | 已完成 |
| 松手到文本 | 触发上传转写到获得 `done` 完整文本的总时长 | `< 15s`（30 秒音频、whisper base） | 服务端精确完成：P50 `1.47 s` / P95 `1.72 s` / P99 `1.72 s`；客户端 2s 轮询感知：P50 `2.02 s` / P95 `2.04 s` / P99 `2.04 s`（`N=10`，错误率 `0%`） | 2026-10-21，Apple M1 Pro、Go API + Worker + Redis + PostgreSQL + SeaweedFS + Whisper base；30.0 秒语音 `.m4a` | `python3 server/scripts/measure-p0-baselines.py`；原始数据 `output/p0-e2e-baseline.jsonl`；报告 [2026-10-21-p0-baseline-report.md](reports/2026-10-21-p0-baseline-report.md) | 已完成 |
| core 耗时占比 | C++ core 处理耗时 / 音频采集时长 | `< 20%`，为 P2 实时化决策提供基线 | 占比：P50 `0.105%` / P95 `0.222%` / P99 `0.623%`（30s 音频 core 耗时 P50 `31.48 ms` / P95 `66.50 ms` / P99 `186.75 ms`，`N=20`，错误率 `0%`） | 2026-10-21，Apple M1 Pro（arm64）；30.0 秒 48 kHz 单声道 S16LE（1,024 samples/帧，共 1,407 帧）重采样至 16 kHz 并写 `.m4a` | `xcodebuild test ... -only-testing:JKMeeting-macOSTests/RecordingPipelineTests`；报告 [2026-10-21-p0-baseline-report.md](reports/2026-10-21-p0-baseline-report.md) | 已完成 |
| 服务端基础链路 | 创建 → S3 PUT → 确认 → 查询 → ASR | 真实 `.m4a` 进入 `done` 并持久化 transcript | 通过 | macOS、Colima、PostgreSQL 16、Redis 7、SeaweedFS 3.68、whisper.cpp base；`say` 生成 AAC/m4a | `make migrate-server && make test-server && make smoke-server && make smoke-asr` | 已验证；性能基线待单独测量 |
| Apple 录音模块 | 双端状态机、采集壳层、m4a 回读、播放控制 | macOS/iOS Simulator 单测、双端构建、Simulator 启动 | 通过 | Xcode 27、iPhone 17 Pro Simulator（iOS 26.2） | macOS/iOS `xcodebuild test`；双端 `xcodebuild build`；`output/ios-recording-interface-stable.png` | 2026-10-04 用户补充确认模拟器 6 秒录制及本地回放正常；Mac/iPhone 物理设备仍待测 |
| 本机合成语音转写复验 | 创建 → 本机 S3 PUT → complete → Worker → Whisper → `done` → 预签名 GET | 单次模拟服务链路成功，不等同 App 录音或性能基线 | 通过（1 次合成音频） | 2026-10-04，Mac/Colima、loopback，`say` 合成约 2.8 秒语音，AAC/m4a | `server/scripts/smoke-asr.sh`：`output/p0-local-asr-smoke.log`；API/Worker/ASR：`output/p0-runtime-{api,worker,whisper}.log`；DB 状态 `done` 且文本已持久化，预签名 GET 返回 200/audio/mp4 | 模拟器 App 已注入运行时配置并重启；用户录音→上传→屏幕出文本仍待操作验证 |

## 3. P1 服务端纵深基线与调优台账（L3 可看 + L4 可扛）

| 指标 / 实验项 | 定义 | 目标/判据 | 实测结果 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 并发阶梯压测（GET / POST） | 100 → 200 → 500 → 1000 并发下的 RPS、P50/P95/P99 与错误率 | 找出吞吐拐点与瓶颈资源 | 吞吐拐点在 **200 并发**：GET `8,121.56 RPS`（P95 `27.75 ms`），POST `4,999.16 RPS`（P95 `49.28 ms`），错误率 `0%`；500 并发进入连接池排队区（POST P95 `122.53 ms`） | 2026-10-21，Apple M1 Pro、Colima、PostgreSQL 16（`DB_MAX_CONNS=30`） | `make load`、`make p1-experiments`；`output/p1-experiments-report.json`；[P1 报告](reports/2026-10-21-p1-server-depth-report.md) | 已完成 |
| `pgxpool` 连接池调优 | 固定 200 并发下扫描 `DB_MAX_CONNS`（2 → 5 → 10 → 25 → 50 → 80） | 找到吞吐饱和点并解释超限下降原因 | `MaxConns=2`：`1,249 RPS`（P95 `209.38 ms`）→ **`MaxConns=10`：尾延迟最优（P95 `69.75 ms`）** → **`MaxConns=25`：吞吐峰值 `4,067.04 RPS`（P50 `43.11 ms`）** → `MaxConns=80`：因 PG 进程上下文切换与锁竞争暴跌至 `1,956.41 RPS`（P99 `524.01 ms`） | 2026-10-21，200 并发 × 2,000 次 `POST /v1/recordings` | `make p1-experiments`；`output/p1-experiments-report.json`；[P1 报告](reports/2026-10-21-p1-server-depth-report.md) | 已完成 |
| 100 万行数据量与慢查询调优 | 100 万条 `recordings` 下无索引、复合索引、深分页 `OFFSET` 与游标分页对比 | `EXPLAIN (ANALYZE, BUFFERS)` 量化索引与游标收益 | 100 万行下：无索引 `Parallel Seq Scan` `45.63 ms`（读 `19,355` 页）vs 复合索引首页 **`0.104 ms`**（读 `7` 页，**快 438x**）；深分页 `OFFSET 50000` 退化为全表扫+落盘排序 `65.81 ms` vs **游标分页（Keyset）`0.231 ms`**（读 `8` 页，**快 285x**） | 2026-10-21，PostgreSQL 16，`1,000,065` 行 `recordings` | `make p1-experiments`；`output/p1-experiments-report.json`；[P1 报告](reports/2026-10-21-p1-server-depth-report.md) | 已完成 |
| 故障注入、限流与 10 万堆积背压 | 令牌桶限流、停 Redis/PG 韧性、10 万 Outbox 积压背压与恢复 | 故障下优雅降级或快速失败，恢复后不雪崩并自动自愈 | ① 突发 100 请求（Burst=25）：25 个 `201` + 75 个 `429`（P50 `2.21 ms`）；② Redis 暂停期间：`POST /complete` 靠 PG Outbox 优雅降级返回 **`200 OK`**，恢复后 `< 1s` 自动补投递为 `dispatched`；③ 10 万条积压超阈值立即返回 `503` 背压保护并按批（64 条/批）平滑消化 | 2026-10-21，Docker Compose `pause/unpause` + 100,000 条 Outbox 积压 | `make p1-experiments`；`output/p1-experiments-report.json`；[P1 报告](reports/2026-10-21-p1-server-depth-report.md) | 已完成 |

## 4. P2 音频管道

| 指标 / 实验项 | 定义 | 目标/判据 | 实测结果 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| core 会话耗时占比（Release，纯 core） | `AudioSession`（时间轴 + sinc 重采样 + 静音检测）处理耗时 / 音频时长 | `< 20%` | 会话：P50 `23.30 ms` / P95 `24.24 ms` / P99 `24.41 ms`，占比 P95 `0.081%`；对照 P0 最近邻 `resample`：P95 `0.48 ms`（`0.0016%`）（`N=20`） | 2026-10-22，Apple M1 Pro，`-O3`（CMake Release）；30 秒 48 kHz 单声道合成信号，每帧 1,024 样本，重采样到 16 kHz | `make bench-core`；`output/p2-1-core-bench.jsonl` | 已完成 |
| core 耗时占比（Xcode Debug，App 链路） | `RecordingPipeline` 中会话与桥接耗时 / 采集时长，含写 m4a 之外的全部 core 调用 | `< 20%` | 占比：P50 `0.854%` / P95 `0.993%` / P99 `1.010%`（core 耗时 P50 `256.10 ms` / P95 `298.04 ms`，`N=20`）；输出时长 `30,000 ms`（与采集时长一致） | 2026-10-22，Apple M1 Pro、Xcode 27.1 RC Debug（`-Onone`）；输入同 P0 基线 | `make test-apple-macos`（`RecordingPipelineTests/testThirtySecondCoreProcessingRatioBaseline`）；`output/p2-1-macos-xctest.log` 中的 `P2_1_CORE_SUMMARY` | 已完成；与 P0 行同环境可比，P95 由 `0.222%` 升至 `0.993%`，换来抗混叠和样本精确的时长 |
| 丢帧补偿 | 每 10 帧丢 1 帧（`sample_time` 连续）后的输出样本数 | 与真实时长对应的样本数相差 ≤ 1 | 3 秒和 30 秒场景均满足；`gap_events` 等于丢帧数 | 合成数据 | `make test-core`（`audio_session_test`）、`make test-apple-macos`（`testCoreSessionFillsDroppedFramesToKeepRealDuration`） | 已完成；真机丢帧率待 P2-3 实测 |
| Opus 编码 RTF（Release，纯 core） | Ogg Opus 编码耗时 / 音频时长（VOIP、24 kbps、20 ms） | RTF P95 `< 1` | P50 `219.96 ms` / P95 `516.87 ms` / P99 `1,029.68 ms`，**RTF P95 `0.017`**（`N=20`） | 2026-10-22，Apple M1 Pro，CMake Release，NEON；30 秒 16 kHz 合成信号；**测量时 load average 约 461** | `make bench-core`；`output/p2-2-opus-bench.jsonl`（`ogg_opus_encode`）；[P2-2 报告](reports/2026-10-22-p2-2-opus-report.md) | 已完成；高负载下的保守上界 |
| 会话加编码占比（Release，纯 core） | `AudioSession` + `OggOpusEncoder` 耗时 / 音频时长 | `< 20%` | P50 `0.850%` / P95 **`1.121%`** / P99 `1.329%`（`N=20`） | 同上；30 秒 48 kHz → 16 kHz → Ogg Opus | `make bench-core`；`output/p2-2-opus-bench.jsonl`（`audio_session_then_ogg_opus`） | 已完成 |
| 会话加编码占比（Xcode Debug，App 链路） | `RecordingPipeline` 计时（自 P2-2 起包含 `writer.append` 中的编码与写文件）/ 采集时长 | `< 20%` | macOS：P50 `2.76%` / P95 `6.17%` / P99 `11.26%`；iOS Simulator：P50 `2.37%` / P95 `2.73%` / P99 `2.78%`；输出时长均为 `30,000 ms`，`55,803 B` | 2026-10-22，Xcode 27.1 RC Debug，链接 Release 版 `Opus.xcframework`；**load average 约 700**；输入同 P0 基线 | `make test-apple-macos` / `make test-apple-ios`；`output/p2-2-{macos,ios}-xctest.log` 中的 `P2_2_CORE_SUMMARY` | 已完成；计时口径与 P2-1 行不同，不能直接比较；低负载下的重测留给 P2-3 |
| Ogg Opus 互通与转写 | FFmpeg 解码长度、ffprobe 时长、Whisper 文本、Apple 原生解码时长 | 时长偏差 ≤ 20 ms，转写与 AAC 无退化 | FFmpeg 解码 `621,534` 帧，与输入**逐样本一致**；ffprobe 偏差 `6.5 ms`（未扣除 pre-skip）；Whisper base 文本与 AAC 32 kbps **完全相同**，体积 `37,570 B`，比 AAC 的 `54,134 B` 小 31%；`AVAudioFile` / `AVAudioPlayer` 在 macOS 与 iOS Simulator 上均为 `3.0 s`（偏差 0） | 12.95 秒 `say` 语音；3 秒正弦（150 个包） | `output/p2-2-interop/`；`OggOpusFileWriterTests` | 已完成；iOS 真机待 P2-3 |
| Opus 波形 SNR（回归护栏） | 解码信号与输入的波形 SNR（按 lookahead 对齐） | `≥ 12 dB`（用户确认：SILK 不保真波形，仅作护栏） | VOIP 24 kbps 多频信号 `13.13 dB`（48 kbps 时也只有 `14.06 dB`）；对照 AUDIO 24 kbps `18.09 dB` | 2 秒 16 kHz 300/700/1300/2100 Hz | `make test-core`（`opus_encoder_test`）；`output/p2-2-interop/snr-probe.txt` | 已完成 |
| Ogg Opus 解码 RTF（Release，纯 core） | `OggOpusDecoder` 解码耗时 / 音频时长（按 4 KiB 分块输入） | RTF P95 `< 1` | 30 秒文件解码 P95 `11.93 ms`，**RTF `0.0004`**；同轮复测编码占比 P95 `0.57%`、会话加编码 `0.66%` | 2026-10-23，Apple M1 Pro，CMake Release；**load average 约 25** | `make bench-core`；`output/p2-3-opus-bench.jsonl`；[P2-3 报告](reports/2026-10-23-p2-3-playback-report.md) | 已完成 |
| Ogg Opus 解码互通 | 与 FFmpeg 对同一文件的解码结果对比 | 帧数一致；数值与 libopus 参考实现一致 | 4 个 FFmpeg libopus 编码文件（16/48 kHz 输入，3 秒 / 7.3 秒）：48 kHz 输出帧数与 FFmpeg、理论值**完全一致**（144,000、350,400）；抽取 2 个文件做数值对比：对 FFmpeg 强制 libopus 解码器最大样本差 `1`，对 FFmpeg 原生解码器 SNR `27.6～31.6 dB` | 同上 | `output/p2-3-interop/run-c3.sh`、`c3-result.txt` | 已完成 |
| Ogg Opus 回放首帧 | `play` 调用到渲染回调输出首个非静音帧（可闻首帧再加输出设备延迟） | 可闻首帧 P95 `< 200 ms` | macOS：P50 `37.4 ms` / P95 `57.2 ms`，可闻 P95 `58.6 ms`；iOS Simulator：P50 `32.5 ms` / P95 `42.9 ms`，可闻 P95 `43.0 ms`（各 `N=20`）；2.5 秒全量回放 `40,000/40,000` 帧，欠载 `0` | 2026-10-23，Xcode 27.1 RC Debug；iPhone 17 Pro Simulator | `make test-apple-macos` / `make test-apple-ios`（`OggOpusFilePlayerTests`）；`output/p2-3-{macos,ios}-xctest.log` | 已完成；真机待测 |
| HLS 转码 RTF | FFmpeg 一次输出三档 AAC fMP4 HLS 的耗时 / 音频时长（`nice 10`） | RTF `< 1`，单并发跟得上上传 | 60 秒：RTF 中位数 `0.0137`；600 秒：`0.0115`（各 3 次）；端到端（下载 + 转码 + 上传）180 秒 `2,663 ms`（RTF `0.015`），23.5 秒 `396 ms` | 2026-10-23，Apple M1 Pro，FFmpeg 8.1；load average 32～62 | `server/scripts/p2-4-hls-bench.py`、`make smoke-hls`；`output/p2-4-hls-bench.jsonl`、`output/p2-4-smoke-hls*.log`；[P2-4 报告](reports/2026-10-23-p2-4-hls-report.md) | 已完成 |
| HLS 档位码率 | 每档分片实际字节 × 8 / 时长 | 与目标偏差 ≤ 10% | 目标 32 / 64 / 96 kbps，实测 `33 / 66 / 100 kbps`（23.5 秒与 180 秒样本一致） | 同上 | `make smoke-hls` | 已完成 |
| 转码对转写延迟的影响 | FFmpeg 并行负载下 Whisper 推理延迟（直接请求 `/inference`，9.5 秒探针，每场景 `N=15`） | P95 劣化不超过 idle 的 20% | idle P95 `447 ms`；1×FFmpeg nice 10：`361 ms`；1×nice 0：`325 ms`；3×nice 0：`276 ms`，**无可测劣化** | 同上；whisper.cpp 走 Metal GPU，场景按顺序执行，背景负载从 38 降到 32 | `server/scripts/p2-4-hls-bench.py`；`output/p2-4-hls-bench.jsonl` | 已完成；差异在噪声范围内，纯 CPU 推理环境待复测 |
| HLS 档位切换 | 设置 `preferredPeakBitRate` 到 access log `indicatedBitrate` 变为目标档的耗时 | 切换可生效并可测 | macOS：33→100 kbps `40～53 ms`，100→66 kbps `17～18 ms`；iOS Simulator：`73 ms` / `18 ms`；23.5 秒短样本起播后全部缓冲，切换不生效 | 2026-10-23，本机回环网络，180 秒录音 | `HLSPlaybackControllerTests`；`output/p2-4-hls-live-macos.log`、`output/p2-4-{macos,ios}-xctest.log` | 已完成；表示开始拉取新档分片，不等于听感切换时刻 |

## 5. P3 Agent 管道

> P3-1 结果为**初步**：评测集标准答案由 AI 起草，尚待用户审核。

| 指标 / 实验项 | 定义 | 目标/判据 | 实测结果 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 纪要要点召回率（文字模式） | 按类别严格匹配的要点覆盖数 / 要点总数（规则评分，不用 LLM 评判） | `≥ 80%` | 20 条：**88.6%**（93/105）；50 次连续调用：88.2%；决议 / 待办 / 观点：91.7% / 90.5% / 81.5% | 2026-10-23，Apple M1 Pro；Qwen2.5-7B-Instruct Q4_K_M，`llama-server -c 8192 -np 1`，温度 0；20 段合成中文会议 | `make eval-insights`；`output/p3-1-text-eval*.json*`；[P3-1 报告](reports/2026-10-23-p3-1-insights-report.md) | 初步通过；待审核答案 |
| 结构化输出格式稳定性 | 连续 50 次调用中的格式失败次数（重试前统计） | `0` | **0/50**，没有发生重试 | 同上 | `make eval-insights EVAL_REPEAT=50`；`output/p3-1-repeat50*.json*` | 已完成 |
| 单条纪要抽取延迟 | 从抽取开始到结果返回（文字模式，单槽位） | P95 `< 8 s` | P50 `12.5 s` / **P95 `15.5 s`**（50 次：`16.3 s`）；生成速度约 24 token/s，平均输出 270 token，约 90% 耗时在生成 | 同上；系统负载 100～190 | `llama-server` 的 `print_timing`；同上 | **未达标**；瓶颈与方案见报告 §3.3 |
| 全链路召回率（音频模式） | `say` 合成 → 上传 → Whisper → LLM，按同一口径评分 | 跑通 ≥ 5 条，报告 ASR 影响 | **28.6%**（8/28）；转写里仍可找到的要点：原始 2/28，繁转简后 12/28 | 5 条，每条 38～44 秒，whisper ggml-base | `server/scripts/p3-audio-eval.sh`、`server/scripts/p3-asr-ceiling.py`；`output/p3-1-audio-*`、`output/p3-1-asr-ceiling.jsonl` | 已完成；瓶颈在 ASR（繁体输出、同音字、人名） |
| 单段纪要 token 消耗 | 每段会议的 prompt / completion token | 记录 | 平均 301 / 270，1 次调用 | 约 250 字、40 秒的会议 | 同上 | 已完成 |
| 纪要抽取延迟（投机解码复测） | 同 E5 口径，加 Qwen2.5-0.5B 草稿模型（接受率 71～77%） | P95 `< 8 s` | P50 `11.4 s` / P95 `13.1 s`，召回率 90.5%（95/105），格式失败 0 | 2026-10-29，负载约 12（与基线 100～190 不同，待同负载复测）；自报生成速度仍约 23.8 token/s | `make run-llm-speculative` + `make eval-insights`；[投机解码报告](reports/2026-10-29-p3-speculative-decoding-report.md) | **未达标（已知缺口，按用户决策保留）** |
| RAG 检索命中率（top-5） | 期望会议进入 top-5 引用候选的比例 | `≥ 90%` | **100%**（20/20），引用命中 100%，越界引用 0 | 2026-10-23，Qwen2.5-7B + bge-m3，20 段合成会议每段 1 块 | `make eval-rag`；`output/p3-2-rag-eval.jsonl`；[P3-2 报告](reports/2026-10-29-p3-2-rag-report.md) | 初步通过；待审核问题集 |
| RAG 答案关键词命中率 | 有答案问题中答案命中关键词的比例 | `≥ 80%` | **95%**（19/20）；无答案 2/2 正确拒答，误拒 0 | 同上，22 题（20 有答案 + 2 无答案） | 同上 | 初步通过；待审核 |
| RAG 问答延迟 | 问题向量化 + 检索 + LLM 作答端到端 | P95 `< 8 s` | 首测 P50 `3.98 s` / P95 `8.35 s`；2026-10-29 投机解码复测 P50 `5.37 s` / **P95 `7.16 s`** | 复测负载低于首测，待同负载确认 | `output/p3-2-rag-speculative-summary.json`；[P3-2 报告](reports/2026-10-29-p3-2-rag-report.md) | 初步达标（复测） |
| MCP 工具可用性 | stdio 协议 initialize/tools/list/tools/call 真实子进程冒烟 | 5 个工具可调用 | 通过：`list_recordings` 连真实 DB 返回数据 | 2026-10-29，本机 | `make test-mcp` | 已完成；Cursor 内真实加载待用户重启确认 |

## 7. P4 Android 与 P5 工程化/实时流

| 指标 / 实验项 | 定义 | 目标/判据 | 实测结果 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| Android 接入 core 改动量 | 为 Android 修改的既有 core 文件数 | 0 或极小 | **0**（`git diff HEAD -- core/` 仅 P5 抖动缓冲在 `CMakeLists.txt` +2 行登记） | 2026-10-29 | `git diff`；[P4 报告](reports/2026-10-29-p4-android-report.md) | 已完成 |
| Android JNI 一致性 | 30 秒 48 kHz 输入经 core 后 16 kHz 输出时长误差 | ≤ 1 帧 | 通过（instrumented 1 测试） | 模拟器 test_avd（android-34 arm64） | `./gradlew connectedDebugAndroidTest` | 已完成 |
| Android 冷启动 | `am start -W` TotalTime；App 内进程创建→首帧 | `< 800 ms` | `am start` 937～1134 ms（模拟器）；App 内 276～535 ms | 同上 | `am start -W` ×3 + logcat | 待真机复测（模拟器口径未达标） |
| 抖动缓冲丢包恢复 | 30% 丢包下输出帧数/时长与静音填充 | 时长误差 ≤ 1 帧 | 1500 帧丢 440（29.3%），输出 1500 帧，填充 440 | 2026-10-29，core 纯单测 | `make test-core`（`jitter_buffer_test`） | 已完成 |
| 实时推流首帧与连续性 | WebSocket 20 ms 帧推流首帧延迟与 30% 丢包连续性 | 首帧 `< 1.5 s`、流不中断 | 首帧 0 ms（同毫秒，回环）；30% 丢包 sent 174、lost 76、5 s 不中断 | 本机回环，合成 440 Hz | `output/p5-stream-smoke.log` | 已完成（非公网口径） |
| 不停机迁移演练 | API 运行中 expand + 回填 + 前后冒烟 | 全程可用 | 通过，回填 8749 行 | 本机 Compose PostgreSQL | `server/scripts/p5-migration-drill.sh` | 已完成 |
| Prometheus 抓取与告警 | target 状态、业务指标可查、规则加载 | 全部生效 | target `up`；`jkmeeting_http_requests_total` 可查；4 条规则加载 | Compose profile `observability` | `promtool check config` + 9090 API | 已完成 |

## 6. 后续阶段预登记

| 阶段 | 指标 | 触发条件 | 当前状态 |
| --- | --- | --- | --- |
| P2 | 真机丢帧率、真机回放首帧、弱网自动切档 | 有真机或网络调节工具时 | P2-1～P2-4 已完成，结果见 §4；真机与弱网项未测 |
| P3 | 结构化输出通过率、任务准确率、端到端 P95、单次成本 | 接入 LLM 或 RAG | P3-1 初步结果见 §5；P3-2 已完成初步评测；P3-3 MCP 已实施（见 §5 末行）；E5/R6 延迟缺口保留 |
| P4 | iOS/Android 启动、采集、播放对比；core 改动量 | Android 接入 | 已完成（模拟器口径）：core 零修改、JNI 一致性通过、冷启动见 §7 |
| P5 | 首帧、丢包恢复、CI 与监控 | 工程化与实时流 | 已完成本机口径：见 §7；CI 待远端首跑、公网弱网未测 |
