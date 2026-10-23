# P2-3 Ogg Opus 解码与回放链路技术方案

- 日期：2026-10-23
- 状态：已实施，待用户终审
- 需求：[P2 音频管道需求 · P2-3](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)
- 决策：用户于 2026-10-23 授权“按推荐方案直接实施”：core 解码，Apple 端用 `AVAudioEngine` + `AVAudioSourceNode` 播放。
- 关联：[ADR 0006](../decisions/0006-opus-ogg-container.md)、[实验报告](../reports/2026-10-23-p2-3-playback-report.md)

## 1. 目标

把 Ogg Opus 回放从系统黑盒（`AVAudioPlayer`）改为“core 解码 + 壳层拉取式渲染”，从而能测量首帧耗时和欠载，并让 P4 的 Android 端复用同一份解码实现。

## 2. 关键取舍

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| Ogg 解析 | core 自研 `OggPacketReader`，流式按页解析，不引入 libogg | 与 P2-2 自研 muxer 对称；页格式只有 CRC、lacing、granule 三个要点 |
| 严格性 | CRC 错误、序号不连续、多 serial、续接标志与实际不符、EOS 时仍有未完成的包，均视为错误 | 本地文件不应出错；宽松解析会掩盖编码器缺陷 |
| 裁剪 | 按 `OpusHead.pre_skip` 裁头（48 kHz 单位换算到输出采样率）；按 EOS 页 granule 裁尾；实际样本少于 granule 声明时报错 | RFC 7845 §4.2、§4.4 |
| 输出采样率 | 默认使用 `OpusHead` 的原始输入采样率（libopus 支持时），可显式指定 | 与编码输入逐样本等长，往返测试可精确比较 |
| 播放模型 | `AVAudioSourceNode` 渲染块从 SPSC 环形缓冲读取 Float；后台线程持续解码写入 | 渲染线程不加锁、不分配内存；可以统计欠载 |
| 缓冲 | 同步预缓冲 100 ms 后启动引擎，环形缓冲容量 4 秒 | 首帧快；4 秒足以吸收解码线程的调度抖动 |
| 分流 | `.ogg` 走新播放器，其他扩展名（历史 m4a）仍用 `AVAudioPlayer` | 不迁移旧录音 |

## 3. 模块与接口

- **core**（纯计算，不做文件 IO）：
  - `jkmeeting::OggPacketReader`：`push(data, size, out)` 输出完整包及其页 granule、BOS/EOS 标记。
  - `jkmeeting::OggOpusDecoder(output_sample_rate = 0)`：
    - `push(data, size, std::vector<int16_t>& out)` / `finish()`；
    - 查询接口：`sample_rate()`、`channels()`、`pre_skip_48k()`、`output_frames()`、`packet_count()`。
  - 工具 `core/tools/ogg_opus_decode`：stdin 输入 Ogg，stdout 输出 s16le PCM，stderr 输出统计 JSON，用于和 FFmpeg 互通对比。
- **桥接** `JKMeetingOggOpusDecoder`：
  - 只提供可失败的 `init`；
  - `decodeData:error:`（Swift 中为 `decode(_:)`）、`finishWithError:`；
  - 属性 `sampleRate`、`channels`、`outputFrames`、`packetCount`。
- **Apple**：
  - `PCMRingBuffer`：基于 `Synchronization.Atomic` 的 SPSC 环形缓冲，读写索引使用 acquire/release 内存序；
  - `OggOpusFilePlayer`：`play(fileURL:onFirstFrame:onFinish:)` / `stop()`，产出 `PlaybackMetrics`；
  - `AppleAudioPlayback`：按扩展名分流，用 `Logger`（category `Playback`）记录指标。

## 4. 首帧口径

- `firstFrameLatencyMs`：从 `play` 记录的 `mach_absolute_time()`，到渲染块第一次从环形缓冲取到解码样本时 `AudioTimeStamp.mHostTime` 的差值（`mHostTime` 无效时退回当前时间）。
- `audibleFirstFrameLatencyMs` = `firstFrameLatencyMs` + `AVAudioOutputNode.presentationLatency`。
- 欠载：解码未结束且渲染块拿到的样本少于请求数的回调次数。

## 5. 错误处理

| 场景 | 处理 |
| --- | --- |
| 文件不可读 | `OggOpusPlaybackError.unreadableFile` |
| 非 Ogg、非 Opus、CRC 错误、截断 | 预缓冲阶段解码失败，抛出 `invalidStream`，不启动引擎 |
| 引擎启动失败 | `engineFailed` |
| 播放中解码失败 | 停止写入，`onFinish` 回调返回错误 |

## 6. 测试

| 层 | 测试 |
| --- | --- |
| core | `ogg_packet_reader_test`：跨页续接与 0 长度 lacing、分块不变性、CRC 错误、魔数与版本错误、续接标志不符、第二个 serial、序号跳变、截断；`ogg_opus_decoder_test`：往返样本数与头部、1/13/4096 字节分块输出一致、显式 48 kHz 与立体声、非 Opus 流与错误头部、截断在 `finish` 时报错 |
| 桥接 | `JKMeetingCoreBridgeTests`：往返、垃圾数据、截断 |
| Apple | `OggOpusFilePlayerTests`：完整播放 2.5 秒且渲染帧数等于解码帧数、20 次首帧基线、拒绝无效文件、ogg/m4a 分流；`PCMRingBufferTests` |
| 互通 | `output/p2-3-interop/run-c3.sh`：FFmpeg libopus 编码的文件经 core 解码，与 FFmpeg 解码对比 |
| 性能 | `make bench-core` 中新增的 `ogg_opus_decode` 管线 |

## 7. 实施修正

| 项 | 原计划 | 实际 | 原因 |
| --- | --- | --- | --- |
| 桥接构造器 | `initWithError:` | 改为可失败的 `init` | ObjC 的 `initWithError:` 导入 Swift 后是 `init() throws`，会被 `NSObject.init()` 遮蔽，导致 `impl_` 为空、XCTest 崩溃（见问题排查记录） |
