# P2-3 Ogg Opus 解码与回放实验报告

- 日期：2026-10-23
- 关联：[P2-3 方案](../plans/2026-10-23-p2-3-opus-playback-implementation.md)、[P2 需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)

## 1. 环境

- Apple M1 Pro，10 核，32 GB 内存，macOS 27.0；Xcode 27.1 RC。
- iOS Simulator：iPhone 17 Pro（`9AB1A395-5D81-4D57-B239-D2D46922AA6C`）。
- FFmpeg 8.1（Homebrew，含 libopus）。
- core 基准测试使用 CMake Release；Apple 测试为 Debug，链接 Release 版 `Opus.xcframework`。测量期间 load average 约 25。

## 2. 命令与原始数据

| 实验 | 命令 | 原始数据 |
| --- | --- | --- |
| core 单测（C1、C2） | `make test-core` | 终端输出（10/10 通过） |
| 解码 RTF（C4） | `make bench-core` | `output/p2-3-opus-bench.jsonl` |
| FFmpeg 互通（C3） | `bash output/p2-3-interop/run-c3.sh` | `output/p2-3-interop/c3-result.txt` |
| Apple 回放（C5、C6） | `make test-apple-macos`、`IOS_SIMULATOR_ID=... make test-apple-ios` | `output/p2-3-macos-xctest.log`、`output/p2-3-ios-xctest.log` |

## 3. 数据

### 3.1 core

- 编码后再解码，样本数与输入完全相等；多频信号的波形 SNR 为 13.12 dB（与 P2-2 对齐后的 13.13 dB 一致）。
- 按 1、13、4096 字节分块输入，输出都与整段输入逐样本一致。

### 3.2 互通（C3）

| FFmpeg libopus 编码的输入 | 我们的解码帧数（48 kHz） | FFmpeg 帧数 | 理论值 | 与 FFmpeg 的 libopus 解码器对比 | 与 FFmpeg 原生解码器对比 |
| --- | --- | --- | --- | --- | --- |
| 16 kHz，3 秒 | 144,000 | 144,000 | 144,000 | 最大样本差 1 | SNR 27.6 dB |
| 48 kHz，3 秒 | 144,000 | 144,000 | 144,000 | — | — |
| 16 kHz，7.3 秒 | 350,400 | 350,400 | 350,400 | — | — |
| 48 kHz，7.3 秒 | 350,400 | 350,400 | 350,400 | 最大样本差 1 | SNR 31.6 dB |

- 以原始采样率输出时，帧数同样等于理论值（16 kHz 输入分别为 48,000 和 116,800 帧）。
- 数值对比只抽取了 2 个文件。
- FFmpeg 原生 Opus 解码器是独立实现，数值上与 libopus 不逐位一致（最大样本差约 530）；强制 FFmpeg 使用 libopus 解码器后，结果只差舍入误差。

### 3.3 性能（C4）

| 管线 | 结果 |
| --- | --- |
| `ogg_opus_decode`（30 秒，4 KiB 分块） | P95 `11.93 ms`，RTF `0.0004` |
| 编码（同轮复测） | 占比 P95 `0.57%` |
| 会话加编码（同轮复测） | 占比 P95 `0.66%` |
| `audio_session` | 占比 P95 `0.079%` |

### 3.4 Apple 回放（C5、C6）

| 平台 | 首帧 P50 / P95 | 可闻首帧 P95 | 2.5 秒完整播放 | 用例 |
| --- | --- | --- | --- | --- |
| macOS | 37.4 / 57.2 ms | 58.6 ms | 渲染 40,000 / 解码 40,000 帧，欠载 0 | 42 个全部通过 |
| iOS Simulator | 32.5 / 42.9 ms | 43.0 ms | 渲染 40,000 / 解码 40,000 帧，欠载 0 | 42 个全部通过 |

## 4. 结论

1. C1～C6 全部通过：core 解码与 FFmpeg（libopus）在帧数上完全一致，在数值上只差舍入。
2. 解码开销可以忽略（RTF 0.0004），回放首帧主要由“预缓冲 100 ms 的解码 + 引擎启动”决定。可闻首帧 P95 不到 60 ms，远低于 200 ms 的目标。
3. 2.5 秒播放 0 欠载，说明 4 秒环形缓冲和后台解码线程足以吸收调度抖动。

## 5. 下一步

- 在 iOS 真机上测量首帧与欠载（Simulator 的音频输出延迟与真机不同）。
- 预缓冲时长可以下调（例如 40 ms），但需要先在真机上评估欠载风险；P2 不做。
