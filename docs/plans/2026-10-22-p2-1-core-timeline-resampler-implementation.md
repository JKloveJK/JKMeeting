# P2-1 技术方案：core 帧时间轴、流式重采样与静音检测

- 日期：2026-10-22
- 需求：[P2 音频管道需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md) 中的 P2-1
- 状态：已实施（2026-10-22 用户确认 core 边界与默认参数），待用户终审

## 实施修订说明

- **抽头数按较低采样率计。** 原方案“单侧 16 个输入抽头”在 48 kHz 下的 Blackman 过渡带宽度约为 \(5.5/32 \times 48\text{ kHz} \approx 8.3\text{ kHz}\)，无法满足 A2 中 10 kHz 衰减 ≥ 40 dB 的要求。改为单侧 `16 × max(1, in/out)` 个输入抽头（48k → 16k 时为 48 个）。
- **统计字段命名。** `max_jitter_ms` 改为 `max_transit_delta_ms`，表示单帧 |到达间隔 − 媒体间隔| 的最大值，避免与平滑后的 `jitter_ms` 混淆。
- **`AudioSession` 接口。** `process` / `finish` 改为 `bool process(view, OwnedAudioFrame& out)`，以区分“被拒绝”和“暂无输出”。
- **`hostTimeNs` 的类型。** Swift 侧用 `Int64?`（与桥接一致），由 `AVAudioTime.seconds(forHostTime:)` 换算为纳秒；`AVAudioTime.hostTime` 本身是 mach tick，不能直接当纳秒用。
- **XcodeGen 源文件。** `project.yml` 改为引用整个 `../core/src` 目录，新增 core 源文件时无需再修改工程声明。

## 1. 架构与数据流

```text
AVAudioEngine tap (AVAudioTime.sampleTime / hostTime)
  -> NativePCMFrameConverter（下混为 S16 单声道，采样率不变）
  -> CapturedPCMFrame { pcm, sampleRate, channels, sampleTime?, hostTimeNs? }
  -> RecordingPipeline
       -> JKMeetingAudioSession (ObjC++)
            -> jkmeeting::audio::AudioSession (C++)
                 FrameTimeline   : 间隙补静音 / 重叠裁剪 / 抖动统计
                 StreamingResampler : 窗函数 sinc，跨帧保留历史与相位
                 apply_gain (可选)
                 SilenceDetector : 20 ms RMS + 挂起时间
       -> AppleAudioFileWriter（不变，仍为 AAC/m4a）
  finish(): session.finish() 冲刷尾部 -> writer.append -> writer.finish；输出 SessionStats
```

`AudioSession` 是 core 中唯一对外的有状态入口；三个组件也单独公开，便于独立测试，P2-2 的 Opus 编码器也会接在同一会话之后。

## 2. 关键设计

### 2.1 StreamingResampler（`core/include/jkmeeting/streaming_resampler.h`）

- 构造参数：`input_rate`、`output_rate`、`channels`；`valid()` 用于校验参数。
- 滤波器：Blackman 窗 sinc，单侧 16 个输入抽头（共 32 个），截止频率取 `0.45 × min(in, out)`（为过渡带预留 10%）。
- 相位：输出样本 `n` 对应输入位置 `n × in / out`。约分后（`L = out / gcd`、`M = in / gcd`）用整数累加器推进 `phase += M`，`while phase >= L` 时推进整数位置，长期运行无浮点漂移。
- 系数表：预计算 `L × 32` 个系数（48k → 16k 时 `L = 1`；44.1k → 16k 时 `L = 160`）。`L > 1024` 时把相位量化到 1,024 档，以限制内存占用。
- 历史：内部 `std::vector<float>` 只保留尚未消费的输入样本及左侧 16 个抽头的历史；起始处用 16 个零样本预填充。
- `flush()`：右侧补 16 个零，按理论总数 `round(total_in × out / in)` 截断输出，保证 A1 成立。
- 输出 `int16_t`，饱和裁剪。

### 2.2 FrameTimeline（`core/include/jkmeeting/frame_timeline.h`）

- 输入：`sample_time`（`int64`，输入采样率下的样本位置；`-1` 表示无效）、`frame_count`、`host_time_ns`（`0` 表示无效）。
- 第一帧确定原点 `origin`，之后 `expected = origin + 已接受样本数`。
  - `sample_time > expected`：判定为间隙，`gap = sample_time - expected`；补静音 `min(gap, max_gap_fill)`，超出部分计入 `unfilled_gap_frames`，并重置原点以避免后续误判。
  - `sample_time < expected`：判定为重叠，裁掉前 `expected - sample_time` 个样本；若整帧重叠则丢弃，并计入 `overlap_frames_discarded`。
  - `sample_time` 无效：按连续流处理。
- 抖动：`D = (arrival_i - arrival_{i-1}) - (media_i - media_{i-1})`，`J += (|D| - J) / 16`（RFC 3550 §6.4.1），单位为 ms；同时记录 `max_abs_transit_delta_ms`。
- 返回值 `TimelineDecision { int64 silence_frames_before; size_t skip_frames; }`，由 `AudioSession` 据此拼接 PCM。

### 2.3 SilenceDetector（`core/include/jkmeeting/silence_detector.h`）

- 配置项 `SilenceConfig { window_ms = 20, threshold_dbfs = -45, hangover_ms = 300 }`。
- 在输出采样率上累积满一个窗口后计算 RMS，并换算为 dBFS（满幅为 32768）。超过阈值记为语音并重置挂起计时；低于阈值时，若挂起计时未耗尽仍记为语音。
- 统计：`speech_ms`、`silence_ms`、`speech_segments`。尾部不满一个窗口的部分在 `finish()` 时按实际长度计入。

### 2.4 AudioSession（`core/include/jkmeeting/audio_session.h`）

```cpp
struct CaptureFrameView { const int16_t* samples; std::size_t sample_count;
                          int sample_rate; int channels; int64_t sample_time; int64_t host_time_ns; };
struct SessionConfig { int input_rate; int output_rate; int channels; float gain_db = 0;
                       int max_gap_fill_ms = 1000; SilenceConfig silence; };
struct SessionStats  { int64_t input_frames, output_frames, gap_events, filled_gap_frames,
                       unfilled_gap_frames, overlap_frames_discarded; double jitter_ms, max_jitter_ms;
                       int64_t speech_ms, silence_ms; };
class AudioSession {
public:
    explicit AudioSession(const SessionConfig&);
    bool valid() const;
    OwnedAudioFrame process(const CaptureFrameView&);   // 失败或参数不匹配时返回空帧并计数
    OwnedAudioFrame finish();                            // 冲刷尾部；之后 process 返回空帧
    const SessionStats& stats() const;
};
```

- 输出帧的 `timestamp_ms = 本帧之前累计的输出样本数 × 1000 / output_rate`，单调且基于样本推导。
- 输入采样率或声道数与配置不一致时拒绝该帧（壳层在格式变化时应重建会话）。

### 2.5 桥接与 Swift

- `apple/Bridge/JKMeetingCore.h/.mm` 新增 `JKMeetingAudioSession`：
  - `-initWithInputSampleRate:channels:outputSampleRate:error:`
  - `-processPCMData:sampleTime:hostTimeNs:error:`，返回 `JKMeetingAudioFrame`（可能是空 PCM，表示尚无输出，不算错误）
  - `-finishWithError:`
  - `stats` 属性，类型为 `JKMeetingAudioSessionStats`
  - 内部用 `std::unique_ptr<AudioSession>` 持有会话；ObjC++ 层负责把 C++ 异常转成 `NSError`。
- `CapturedPCMFrame` 新增 `sampleTime: Int64?` 和 `hostTimeNs: UInt64?`（带默认值，现有调用点不受影响）。`AppleAudioCapture.forward` 从 `AVAudioTime` 填入这两个字段：`sampleTime` 取硬件采样率下的值；`NativePCMFrameConverter` 不改变采样率，所以可以直接使用。
- `RecordingPipeline`：用 `AudioSessionProcessing` 协议抽象会话（便于测试注入），在第一帧到来时按其格式创建会话；空输出不写文件；`finish()` 先写入会话尾部再完成写文件；新增 `lastSessionStats` 属性并输出一条日志。

## 3. 错误处理

| 情况 | 处理 |
| --- | --- |
| 会话参数非法 | `valid() == false`；桥接层返回 `JKMeetingCoreErrorInvalidInput` |
| 帧格式与会话不一致 | 返回空帧并计入 `rejected_frames`；Swift 抛出 `.invalidPCM`，沿用现有的写入器丢弃流程 |
| 超大间隙 | 只补到上限，其余计入 `unfilled_gap_frames`，录音不中断 |
| 内存分配失败 | 捕获 `std::bad_alloc`，返回 `allocationFailed` |

## 4. 测试策略（TDD）

- `core/tests/streaming_resampler_test.cpp`：A1 的样本数与分块一致性；A2 的通带（1 kHz）和阻带（10 kHz）；非法参数。
- `core/tests/frame_timeline_test.cpp`：间隙补齐、超上限、重叠裁剪、整帧重叠、无效 `sample_time`、抖动数值（构造固定的到达偏差，与手算结果比较）。
- `core/tests/silence_detector_test.cpp`：全静音、全语音、交替段与挂起时间、尾部不满窗口。
- `core/tests/audio_session_test.cpp`：端到端：丢帧场景下输出时长等于真实时长、时间戳单调、`finish` 之后拒绝新帧。
- `core/bench/audio_session_bench.cpp`：30 秒 48 kHz 单声道，每帧 1,024 样本，重复 20 次，输出 P50/P95/P99 耗时和占比（JSON 一行）；新增 `make bench-core`。
- `apple/BridgeTests/JKMeetingCoreBridgeTests.mm`：会话的创建、处理、结束和统计。
- `apple/SharedTests/RecordingPipelineTests.swift`：使用注入的会话，验证尾部写入和统计暴露；保留现有耗时测量。

## 5. 文件清单

| 操作 | 路径 |
| --- | --- |
| 新增 | `core/include/jkmeeting/{streaming_resampler,frame_timeline,silence_detector,audio_session}.h` |
| 新增 | `core/src/{streaming_resampler,frame_timeline,silence_detector,audio_session}.cpp` |
| 新增 | `core/tests/{streaming_resampler,frame_timeline,silence_detector,audio_session}_test.cpp`、`core/bench/audio_session_bench.cpp` |
| 修改 | `core/CMakeLists.txt`、`Makefile`（`bench-core`）、`core/README.md` |
| 修改 | `apple/project.yml`（新的 core 源文件）、`apple/Bridge/JKMeetingCore.{h,mm}`、`apple/BridgeTests/JKMeetingCoreBridgeTests.mm` |
| 修改 | `apple/Shared/Recording/{AudioCapture,AppleAudioCapture,RecordingPipeline}.swift`、`apple/SharedTests/RecordingPipelineTests.swift` |
| 新增 | `docs/decisions/0005-core-streaming-audio-session.md`（有状态会话、时间轴策略、对 ADR 0002 的补充） |
| 修改 | `docs/metrics.md`、`docs/plans/2026-09-29-p0-minimal-loop-design.md`（转码 / HLS 归属从 P5 改为 P2，与路线文档一致） |

## 6. 实施步骤

1. 编写重采样器测试（预期失败）→ 实现 → `make test-core`。
2. 时间轴、静音检测、会话按同样方式逐个完成。
3. 基准程序与 `make bench-core`，记录原始输出到 `output/p2-1-core-bench.jsonl`。
4. 桥接与桥接测试 → `make test-apple-bridge`。
5. Swift 接入与 XCTest → `make test-apple-macos`；视条件运行 iOS 构建。
6. 更新 ADR、README 和指标台账，汇报验证结果与剩余风险。

## 7. 风险

- sinc 滤波会引入 16 个输入样本的群延迟（48 kHz 下约 0.33 ms），对录音场景可忽略；P5 实时流时需要重新评估。
- `AVAudioTime.sampleTime` 在路由切换（例如插拔耳机）后可能重置，时间轴会把它识别为重叠或大间隙。P2-1 只计数，不处理路由切换（P0 范围内本来就没有处理）。
- 抖动指标反映的是“采集回调到达的抖动”，不是网络抖动；网络侧抖动缓冲留到 P5 实时流。
