# JKMeeting C++ core

`core/` 是 iOS 与后续 Android 共享的纯 C++ 音频处理模块。它只包含纯计算、帧数据和状态逻辑；不依赖平台 SDK，不包含采集、播放、文件 IO、网络或 UI。唯一的第三方依赖是 libopus（源码随仓库提供，见 [third_party/README.md](third_party/README.md)）。

## 契约

- 输入：`AudioFrameView` / `CaptureFrameView` 借用调用方的交错 S16LE PCM，输入缓冲区只需在本次调用期间有效。
- 输出：`OwnedAudioFrame` 用 `std::vector<int16_t>` 持有 PCM，返回后可以安全交给 ObjC++ / JNI 桥接层继续使用。
- 无效输入（空 PCM、非正的采样率或声道数、采样数不能被声道数整除）不会产生输出。

完整所有权决策见 [ADR 0002](../docs/decisions/0002-core-audio-frame-ownership.md)，有状态会话见 [ADR 0005](../docs/decisions/0005-core-streaming-audio-session.md)，Opus 编码与 Ogg 封装见 [ADR 0006](../docs/decisions/0006-opus-ogg-container.md)。

## 组件

| 头文件 | 职责 |
| --- | --- |
| `audio_session.h` | 录音主链路入口：时间轴 → 流式重采样 → 增益 → 静音检测；输出时间戳由累计输出样本数推导；`finish()` 冲刷尾部并返回会话统计 |
| `frame_timeline.h` | 以采集样本位置 `sample_time` 检测间隙（补静音，默认上限 1,000 ms）和重叠（裁剪）；按 RFC 3550 统计到达抖动 |
| `streaming_resampler.h` | Blackman 窗 sinc 流式重采样；跨调用保留历史与相位，分块方式不影响输出；整段输出样本数为 `round(N × out / in)` |
| `silence_detector.h` | 20 ms 窗口 RMS 判定，阈值 −45 dBFS，挂起 300 ms；统计语音和静音时长 |
| `audio_processor.h` | P0 遗留的无状态最近邻重采样与 dB 增益；增益仍被会话复用，重采样已不在主链路中 |
| `ogg_opus_encoder.h` | 录音编码入口：PCM → Ogg Opus 字节流（`headers()` → `encode()` → `finish()`）；输出与分块方式无关，时长由输入样本数推导 |
| `opus_encoder.h` | libopus 薄封装（VOIP、VBR、默认 24 kbps / 20 ms）；`flush()` 补零编码到覆盖 lookahead |
| `ogg_opus_muxer.h` | RFC 3533 / RFC 7845 字节生成：OpusHead / OpusTags、lacing、CRC、granule；最后一个包与 EOS 同页，末页 granule 用于裁掉补零 |
| `ogg_packet_reader.h` | 流式 Ogg 页解析：校验 CRC、序号、单一 serial 与续接标志，拼接跨页包；输出包及其页 granule、BOS/EOS（P2-3） |
| `ogg_opus_decoder.h` | 回放解码入口：Ogg Opus 字节流 → S16 PCM（`push()` → `finish()`）；按 `pre_skip` 裁头、按末页 granule 裁尾，默认输出 OpusHead 记录的原始采样率 |

会话与编码器对象都不是线程安全的，需要串行调用。`sample_time < 0` 表示壳层无法提供采集位置，此时按连续流处理；`host_time_ns == 0` 表示不参与抖动统计。

## 运行与测试

从仓库根目录执行：

```sh
make test-core    # CMake 配置、构建，并通过 CTest 运行全部单元测试
make bench-core   # Release 构建 30 秒会话、Ogg Opus 编码与解码基准，结果追加到 output/p2-3-opus-bench.jsonl（可用 BENCH_CORE_OUTPUT 覆盖）
make opus-xcframework  # 为 Apple 构建 Opus.xcframework（版本戳一致时跳过）
```

命令行工具 `ogg_opus_encode`（随 `make test-core` 构建）从 stdin 读取 S16LE PCM，向 stdout 输出 `.ogg`，用于互通验证和 `AUDIO_FORMAT=ogg make smoke-asr`：

```sh
core/build/ogg_opus_encode --rate 16000 --channels 1 --bitrate 24000 < in.pcm > out.ogg
```

对应的 `ogg_opus_decode` 从 stdin 读取 `.ogg`，向 stdout 输出 S16LE PCM，并在 stderr 输出统计 JSON（采样率、声道、帧数、包数、`pre_skip`），用于和 FFmpeg 对比（P2-3 C3）：

```sh
core/build/ogg_opus_decode [--rate 48000] < in.ogg > out.pcm
```

`CMakeLists.txt` 在构建类型为空时默认使用 Debug，因为 libopus 会把空构建类型改成 Release，从而让测试中的 `assert` 失效。
