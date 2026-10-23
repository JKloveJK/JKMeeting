# 0005：C++ core 引入有状态音频会话，以采集样本位置维护时间轴

- 日期：2026-10-22
- 状态：已接受
- 关联：[P2 需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)、[P2-1 方案](../plans/2026-10-22-p2-1-core-timeline-resampler-implementation.md)、[ADR 0002](0002-core-audio-frame-ownership.md)

## 背景

P0 的 `AudioProcessor::resample` 是逐帧、无状态的最近邻抽取：每帧输出 `floor(frames × out / in)` 个样本，小数部分被丢弃（48 kHz → 16 kHz、每帧 1,024 样本时，每帧少 0.33 个样本），也没有抗混叠滤波。时间戳只是透传壳层传入的毫秒值；`AppleAudioCapture` 背压丢帧后，时间轴直接塌缩，录音文件比真实时长短。要修正这些问题，必须跨帧保留滤波历史、小数相位和期望的样本位置，无状态 API 无法表达。

## 决定

- core 新增 `AudioSession`（一次录音对应一个会话），由 `FrameTimeline`、`StreamingResampler`、增益和 `SilenceDetector` 组合而成；四个组件均可单独使用和测试。
- 壳层向 core 传递 **采集样本位置 `sample_time`**（输入采样率下的样本数）和 **到达时刻 `host_time_ns`**，不再只传毫秒时间戳。core 以样本位置判定间隙和重叠：间隙补静音（单次上限默认 1,000 ms，超出部分只计数），重叠部分裁剪。输出时间戳由累计输出样本数推导。
- 重采样采用 Blackman 窗 sinc 插值；单侧抽头数按较低采样率计 16 个，降采样时按比例放大；相位用约分后的整数累加器推进，长期运行无漂移。
- 桥接层新增 `JKMeetingAudioSession`，在内部用 `std::unique_ptr` 持有 C++ 会话。旧的 `AudioProcessor` 与 `resamplePCMData` 保留，不再被录音主链路使用。
- 继续遵守 ADR 0002：输入仍是借用视图，只在单次 `process` 调用期间有效。会话内部的历史样本由会话自行复制并持有，不保存调用方指针。

不选择：

- 用 `AVAudioConverter` 在壳层重采样：Android 侧需要再实现一遍，违背“core 是跨端唯一实现”的路线判据。
- 引入 libsamplerate / speexdsp：P2 只允许 Opus 这一个第三方库；windowed-sinc 在 core 内约 150 行即可满足验收。
- 用毫秒时间戳检测丢帧：1 ms 在 48 kHz 下等于 48 个样本，精度不足，取整误差会被误判为间隙或重叠。

## 依据

- `make test-core`：分块不变性（1,024 / 480 / 不规则分块下输出逐样本一致）、样本数误差 ≤ 1、1 kHz 通带偏差 ≤ 1 dB、10 kHz 阻带衰减 ≤ −40 dB、丢帧后输出时长等于真实时长。
- `make bench-core`（Release，M1 Pro）：30 秒 48 kHz 音频的会话处理 P95 为 `24.24 ms`，占采集时长 `0.081%`；最近邻旧实现为 `0.48 ms`。代价约增加 50 倍，但仍远低于 20% 的预算。原始数据见 `output/p2-1-core-bench.jsonl`。

## 后果

- 壳层必须保证同一会话内输入格式不变；格式变化时需要重建会话（当前 `AVAudioEngineConfigurationChange` 会直接停止录音，满足该前提）。
- sinc 滤波引入约 0.33 ms 群延迟（48 kHz 输入、48 个抽头的一半）；P5 做实时流时需要重新评估延迟预算。
- `AVAudioTime.sampleTime` 在路由切换后可能重置，时间轴会把它识别为重叠或超大间隙；P2-1 只计数，不做恢复。
- P2-2 的 Opus 编码器接在会话输出之后，不改变本决定的时间轴语义。
