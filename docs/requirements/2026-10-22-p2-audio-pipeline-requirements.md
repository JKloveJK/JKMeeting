# P2 音频管道需求

- 日期：2026-10-22
- 状态：P2-1 已实施，待用户终审（core 边界与默认参数已于 2026-10-22 确认）；P2-2 已实施，待用户终审（B1～B7 已验证；B2 的 SNR 门限经用户确认调整）；P2-3 已实施，待用户终审（C1～C6 已验证）；P2-4 已实施，待用户终审（D1～D6 已验证）
- 关联：[客户端全栈学习路线 §3 P2](../客户端全栈学习路线.md)、[P0 设计](../plans/2026-09-29-p0-minimal-loop-design.md)、[ADR 0002](../decisions/0002-core-audio-frame-ownership.md)、[指标台账](../metrics.md)

## 背景与问题

P0 打通了「采集 → core → m4a → 上传 → 转写 → 回放」闭环，P1 把服务端推进到 L3/L4。音频链路本身仍停留在“能用”的层次，源码中可以确认以下问题：

1. **重采样有累积误差且无抗混叠。** `core/src/audio_processor.cpp` 中的 `AudioProcessor::resample` 逐帧无状态地做最近邻抽取，输出样本数为 `floor(input_frames × target / source)`，小数部分被丢弃。基线场景下每帧有 1,024 个样本（48 kHz → 16 kHz），每帧应得 341.33 个样本，实际只输出 341 个；30 秒共 1,407 帧，理论上累计丢失约 469 个样本（约 29 ms）。抽取前也没有低通滤波，高于 8 kHz 的分量会混叠回语音频带。
2. **时间戳只是透传。** core 只把壳层传入的 `timestamp_ms` 原样复制到输出，不维护基于样本计数的时间轴，也不检测时间戳的跳变或回退。
3. **丢帧会让时间轴塌缩。** `AppleAudioCapture.forward` 在 `pendingFrameSlots` 已满或转换失败时直接丢帧，只累加 `droppedFrames`；写入文件时没有补偿，录音文件因此短于实际时长，后续音频与文本的时间对齐也会出现偏差。
4. **没有静音检测。** 无法统计语音占比，也无法为后续转写裁剪静音和 P5 实时分段提供依据。
5. **编码与回放仍依赖平台原生 AAC/m4a。** 服务端只保存原始文件，没有转码和多码率回放。

## 目标

按路线文档完成 P2「音频管道」，让音频全链路可以测量：

- C++ core 具备有状态的帧与时间戳管理、带抗混叠的重采样、增益、静音检测，以及丢帧与抖动处理。
- 用 Opus（libopus 源码放入 `core/third_party/`）替换平台原生编码。
- 服务端引入 FFmpeg 转码，生成多码率 HLS；Apple 端可以切换码率回放。

阶段验收：转码与编码 RTF < 1；core 处理耗时占比 < 20%；回放可在多个码率间切换。

## 子需求拆分与顺序

按“依赖最少、风险最低的先做”串行推进（用户已确认）。每个子需求完成并通过用户终审后，再启动下一个。

| 编号 | 内容 | 模块 | 状态 |
| --- | --- | --- | --- |
| P2-1 | core 帧与时间戳、流式重采样、静音检测、丢帧/抖动处理 | `core/`、`apple/Bridge`、`apple/Shared/Recording` | 已实施，待用户终审 |
| P2-2 | 引入 libopus，在 core 中编码；客户端落盘格式改为 Opus | `core/`、`apple/`、`server/`（Content-Type / object key） | 已实施，待用户终审（B1～B7 已验证） |
| P2-3 | 回放链路：Opus 解码回放、首帧耗时测量 | `core/`、`apple/` | 已实施，待用户终审（C1～C6 已验证） |
| P2-4 | 服务端 FFmpeg 转码 Worker → 多码率 HLS；Apple 端切换码率回放；转码并发控制与队列优先级 | `server/`、`apple/`（`docker-compose.yml` 经用户确认不改，使用宿主机 FFmpeg） | 已实施，待用户终审（D1～D6 已验证） |

## P2-1 范围（本次实施）

1. **流式重采样器**：有状态，跨帧保留滤波历史与小数相位；使用窗函数 sinc 低通插值；任意分块方式下输出逐样本一致；整段输出样本数与理论值相差不超过 1。
2. **帧时间轴**：以壳层提供的采集样本位置（`sample_time`，单位为输入采样率下的样本数）为准，计算期望位置并检测间隙与重叠。间隙用静音补齐（设上限），重叠部分裁掉；输出时间戳由累计输出样本数推导，保证单调。壳层不提供 `sample_time` 时，按连续流处理。
3. **抖动统计**：根据壳层提供的到达时刻（`host_time_ns`）与媒体时间差，按 RFC 3550 的平滑方式计算到达抖动，记录平均值与最大值。
4. **静音检测**：在输出采样率上按 20 ms 窗口计算 RMS（dBFS），用阈值加挂起时间（hangover）判定语音或静音，统计语音时长与占比。
5. **会话聚合**：`AudioSession` 串联“时间轴 → 重采样 → 增益（可选）→ 静音检测”，结束时冲刷重采样尾部，并返回会话统计。
6. **Apple 接入**：ObjC++ 桥接新增会话对象；`CapturedPCMFrame` 携带 `sampleTime` 与 `hostTimeNs`；`RecordingPipeline` 改为使用会话，结束时写入尾部样本并在本地日志与对象上暴露统计。
7. **测量**：新增 core 基准程序，复测 core 耗时占比；更新指标台账。

## P2-1 非目标

- 不改变上传格式（仍为 AAC/m4a），不新增服务端字段，统计结果暂不上报服务端（上报涉及 schema，留到需要时再定）。
- 不引入 WebRTC VAD、降噪、AGC、回声消除或任何第三方 DSP 库。
- 不改变录音状态机、5 分钟上限和权限流程。
- 不处理 Android（P4）。

## 约束

- `core/` 只放纯计算，不得引入平台头文件、文件 IO 或线程。
- 保持 ADR 0002 的所有权语义：输入为借用视图，输出为拥有的缓冲区。会话对象内部持有的历史样本由会话自行拥有，不保存调用方指针。
- 现有 `AudioProcessor` 接口保留，避免破坏桥接测试与已有调用方。
- 测试沿用 `core/tests/` 现有的 assert 风格，不新增测试框架依赖。

## P2-1 验收标准

| 编号 | 标准 | 证据 |
| --- | --- | --- |
| A1 | 48 kHz → 16 kHz、44.1 kHz → 16 kHz，按 1,024 / 480 / 不规则分块喂入 30 秒数据，总输出样本数与 `round(N × out / in)` 相差 ≤ 1；不同分块方式的输出逐样本一致 | `make test-core` |
| A2 | 48 kHz 下 1 kHz 正弦重采样后幅度偏差 ≤ 1 dB；10 kHz 正弦（高于 16 kHz 的 Nyquist 频率）重采样后残余能量 ≤ −40 dB | `make test-core` |
| A3 | 构造 `sample_time` 跳变（模拟丢帧），输出时长等于真实采集时长；回退或重叠时不重复写入；无 `sample_time` 时按连续流处理 | `make test-core` |
| A4 | 静音、语音段（合成正弦 / 噪声）交替输入时，语音时长误差 ≤ 一个窗口加挂起时间 | `make test-core` |
| A5 | 30 秒 48 kHz 单声道（每帧 1,024 样本）的 core 会话耗时占比 P95 < 20%，并写入指标台账 | core 基准程序输出 + `docs/metrics.md` |
| A6 | 桥接测试与 macOS XCTest 通过；`RecordingPipeline` 结束时写入尾部样本并输出统计 | `make test-apple-bridge`、`make test-apple-macos` |

## P2-2 范围（Opus 编码）

1. 把 libopus 1.6.1 源码放入 `core/third_party/opus/`（用户已确认），由 core 的 CMake 构建；Apple 端通过预编译的 xcframework 链接。
2. core 新增 Opus 包编码器与 Ogg Opus 封装器：只生成内存中的字节，文件写入留在壳层。
3. Apple 录音主链路改为会话 PCM → Opus → `.ogg` 文件，上传使用 `audio/ogg`。
4. 服务端把 `content_type` 改为白名单（`audio/mp4`、`audio/ogg`），对象键扩展名随类型变化；Worker 和 ASR 不改。
5. 容器格式先调研再评审（用户要求）：调研结论与证据见 [P2-2 技术方案 §2](../plans/2026-10-22-p2-2-opus-encoding-implementation.md)。

## P2-2 非目标

- 不在 core 中实现 Opus 解码或 Ogg 解析（回放改动属于 P2-3）。
- 不迁移已有的 m4a 录音，不改数据库 schema。
- 不构建 Intel Mac 和 x86_64 模拟器的产物。
- 不启用 DRED、OSCE 或深度 PLC 等神经网络特性。

## P2-2 验收标准

| 编号 | 标准 | 证据 |
| --- | --- | --- |
| B1 | Ogg 页结构符合 RFC 3533 / RFC 7845：CRC 正确，BOS / EOS 标志、`OpusHead` / `OpusTags` 和 granule 均正确 | `make test-core` |
| B2 | 编码后解码回放的长度与输入一致（裁掉 `pre_skip` 后）；语音频段多频信号（300/700/1300/2100 Hz）的波形 SNR ≥ 12 dB，仅作回归护栏；分块方式不影响输出字节 | `make test-core` |
| B3 | 30 秒 16 kHz 单声道编码 RTF P95 < 1（目标远小于 1），会话加编码的总耗时占比 < 20% | `make bench-core`（`output/p2-2-opus-bench.jsonl`） |
| B4 | 工具生成的 `.ogg` 能被 `ffprobe` 识别为 opus，时长偏差 ≤ 20 ms；同一段语音的 Whisper 转写结果与 m4a 一致或差异可解释 | 实验报告 |
| B5 | macOS 与 iOS Simulator 上，`OggOpusFileWriter` 写出的文件能被 `AVAudioFile` / `AVAudioPlayer` 读取，时长偏差 ≤ 20 ms | `make test-apple-macos`、`make test-apple-ios` |
| B6 | 服务端接受 `audio/ogg`，对象键为 `audio.ogg`；未知类型返回 `400` | `make test-server` |
| B7 | Ogg 文件经端到端冒烟进入 `done` 并得到转写文本 | `AUDIO_FORMAT=ogg make smoke-asr` |

## P2-3 范围（回放链路）

现状：`AppleAudioPlayback` 用 `AVAudioPlayer` 播放本地文件，Ogg Opus 由系统解码（P2-2 B5 已验证可行）。回放链路因此是黑盒：无法测首帧耗时，也无法观察欠载；P4 的 Android 端也不能复用这部分。

1. **core 解码**：新增 Ogg 页解析器（校验 CRC，处理跨页包和 255 续接 lacing）和 Opus 包解码器（复用已 vendored 的 libopus，无新依赖）。按 `OpusHead` 的 `pre_skip` 裁头、按末页 granule 裁尾，输出与编码输入逐样本等长的 PCM。core 仍只处理内存字节，读文件由壳层负责。
2. **Apple 回放**：新增基于 `AVAudioEngine` + `AVAudioSourceNode` 的拉取式播放器。壳层读文件，把字节交给 core 解码，PCM 写入预缓冲队列，渲染回调按需取数。已有的 m4a 录音仍走 `AVAudioPlayer`（按扩展名分流）。
3. **首帧耗时**：从调用播放到渲染回调第一次输出非静音样本的主机时间，再加上设备输出延迟（`outputLatency`），记为“可闻首帧耗时”。同时记录欠载次数（渲染时队列为空的回调数）。
4. **测量**：core 解码 RTF 写入 bench；Apple 端在 macOS 和 iOS Simulator 上各跑 20 次首帧测量，记录 P50/P95。

## P2-3 非目标

- 不做 seek、倍速、后台播放、锁屏控制或远程流式回放（远程回放属于 P2-4 的 HLS）。
- 不做 PLC 或丢包隐藏（本地文件不丢包）。
- 不改服务端。

## P2-3 验收标准

| 编号 | 标准 | 证据 |
| --- | --- | --- |
| C1 | Ogg 解析器能解析本项目编码器输出，以及含跨页包、多包页、0 长度 lacing 的构造输入；CRC 错误、截断、非 Opus 流返回错误 | `make test-core` |
| C2 | core 编码 → 解码往返后样本数与输入完全相等；SNR ≥ 12 dB（回归护栏）；分块喂入字节的方式不影响输出 | `make test-core` |
| C3 | 用 FFmpeg（libopus）生成的 `.ogg` 经 core 解码后，样本数与 FFmpeg 自身解码结果一致 | 实验报告（`ogg_opus_decode` 工具） |
| C4 | 30 秒 16 kHz 单声道解码 RTF P95 < 1 | `make bench-core` |
| C5 | macOS 与 iOS Simulator 上，新播放器播放 `.ogg` 的总渲染样本数与文件时长偏差 ≤ 20 ms，正常播放欠载次数为 0；m4a 仍可播放 | `make test-apple-macos`、`make test-apple-ios` |
| C6 | 记录首帧耗时 P50/P95 基线（各 20 次），写入指标台账；目标 P95 < 200 ms | XCTest 输出 + `docs/metrics.md` |

## P2-4 范围（服务端 HLS 转码）

现状：上传完成后只派发转写任务（`transcription_outbox`，`recording_id` 唯一）；Worker 使用 asynq，`Concurrency: 1`，单队列；回放接口 `GET /v1/recordings/{id}/playback` 返回原始文件的预签名 URL。本机已有 Homebrew FFmpeg 8.1（含 `aac` 和 `libopus`），Whisper 已依赖它。

1. **转码任务**：上传确认时，在同一事务内写入转码 outbox，由 dispatcher 投递到独立的 asynq 队列；转码 Worker 下载原始文件，调用 FFmpeg 生成多码率 HLS（master playlist + 各档 media playlist + 分片），上传到 `recordings/<id>/hls/`。
2. **状态与接口**：新增转码状态（`pending` / `processing` / `ready` / `failed`），与转写状态互不阻塞。回放接口在 HLS 就绪时额外返回 master playlist 地址。
3. **并发与优先级**：转写与转码使用不同的 asynq 队列，转码队列由独立的 `asynq.Server` 消费并单独设置并发上限，FFmpeg 以 `nice` 降低 CPU 优先级，避免 FFmpeg 占满 CPU 拖慢转写。（原写“设置权重”；asynq 的队列权重只影响出队概率，无法限制并发，实施时改为上述机制，见 ADR 0007。）
4. **Apple 端**：远程回放使用 `AVPlayer` 播放 HLS，可手动切换码率（通过 `preferredPeakBitRate` 或直接选择档位），并记录切换生效耗时。
5. **测量**：转码 RTF、并发 N 下转写 P95 是否受影响，写入实验报告。

## P2-4 非目标

- 不做视频、DRM、CDN 或 HTTPS。
- 不做直播（实时 HLS），只做录音结束后的点播转码。
- 不迁移历史录音（可提供手动补转码脚本，但不自动执行）。

## P2-4 验收标准

| 编号 | 标准 | 证据 |
| --- | --- | --- |
| D1 | 上传确认后转码状态最终为 `ready`，S3 中存在 master playlist 和每档的 playlist 与分片；转写结果不受影响 | `make smoke-hls`（新增） |
| D2 | master playlist 声明的码率档位与配置一致；`ffprobe` 能解析每一档 | 冒烟脚本断言 |
| D3 | 转码 RTF P95 < 1（30 秒样本） | 实验报告 |
| D4 | 转码失败（FFmpeg 不存在、输入损坏）时状态为 `failed` 且有错误信息；可重试错误按 asynq 重试 | `make test-server` |
| D5 | 转码与转写同时压测时，转写 P95 的劣化在报告中量化，并说明队列隔离、并发上限与 `nice` 的作用 | 实验报告 |
| D6 | Apple 端可以在多个码率间切换回放，切换后 `AVPlayerItem` 的 access log 显示对应码率 | XCTest 或手动验证记录 |

## 依赖

- 本机 CMake / Xcode 27 工具链（已具备）。
- iOS Simulator 测试需要 `IOS_SIMULATOR_ID`；若本轮不运行，交付时会说明。

## 待确认问题

以下三项已于 2026-10-22 由用户确认，均按建议执行。

1. **core 与壳层边界变更（必须由人确认）**：桥接层新增 `JKMeetingAudioSession`（有状态会话对象），壳层改为传入 `sampleTime`（样本位置）和 `hostTimeNs`，不再只传毫秒时间戳。旧的 `resamplePCMData` 保留。
2. **间隙补静音上限**：建议单次最多补 1,000 ms；超过上限视为中断（例如系统打断），只补到上限，其余计入“未补偿间隙”。
3. **静音检测默认参数**：建议阈值 −45 dBFS、挂起 300 ms、窗口 20 ms，并做成可配置项。

以下 P2-3 / P2-4 决策于 2026-10-23 由用户授权“按推荐方案直接实施”：

4. **P2-3 回放方案**：core 负责 Ogg 解析与 Opus 解码，Apple 端用 `AVAudioEngine` + `AVAudioSourceNode` 拉取式播放；m4a 仍走 `AVAudioPlayer`。
5. **P2-4 编码与档位**：AAC-LC fMP4 HLS，三档 32 / 64 / 96 kbps，每片 6 秒。
6. **P2-4 数据模型（schema 变更）**：新增 `transcode_jobs` 表和 `transcode_outbox` 表（迁移 `0003_add_transcode_jobs.sql`），`recordings` 表不变。
7. **P2-4 依赖**：使用宿主机 FFmpeg（`FFMPEG_PATH`），`docker-compose.yml` 不变。

## 验收结果

| 编号 | 结果 | 证据 |
| --- | --- | --- |
| C1、C2 | 通过；往返 SNR `13.12 dB` | `make test-core` |
| C3 | 帧数与 FFmpeg 完全一致；与 FFmpeg 的 libopus 解码器最大样本差 1 | `output/p2-3-interop/c3-result.txt` |
| C4 | 解码 RTF `0.0004` | `output/p2-3-opus-bench.jsonl` |
| C5 | macOS / iOS Simulator 渲染 `40,000/40,000` 帧，欠载 0；m4a 回放路径保留 | `output/p2-3-{macos,ios}-xctest.log` |
| C6 | 可闻首帧 P95：macOS `58.6 ms`，iOS Simulator `43.0 ms` | `docs/metrics.md` |
| D1、D2 | 三档就绪，每档 ffprobe 为 `aac,48000,1`，实测码率 33 / 66 / 100 kbps；转写同时到达 `done` | `make smoke-hls`；`output/p2-4-smoke-hls*.log` |
| D3 | 转码 RTF 中位数 0.0115～0.0137；端到端 RTF 0.015～0.017 | [P2-4 报告](../reports/2026-10-23-p2-4-hls-report.md) |
| D4 | FFmpeg 缺失或失败直接 `failed`，临时错误重试、第 3 次失败才 `failed` | `go test ./internal/transcode/` |
| D5 | 本机无可测劣化（Whisper 走 Metal GPU）；机制定位为结构性隔离 | [P2-4 报告](../reports/2026-10-23-p2-4-hls-report.md) §4 |
| D6 | macOS / iOS Simulator 上 access log 均显示目标档位，切换 17～73 ms | `HLSPlaybackControllerTests` |
