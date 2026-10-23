# P2-2 Opus 编码与 Ogg 封装实验报告

- 日期：2026-10-22
- 关联：[需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)、[方案](../plans/2026-10-22-p2-2-opus-encoding-implementation.md)、[ADR 0006](../decisions/0006-opus-ogg-container.md)

## 1. 实验环境

- 机器：Apple M1 Pro，macOS 27.0；Xcode 27.1 RC（macOS SDK 27.0、iOS SDK 27.1）；iOS Simulator `9AB1A395-5D81-4D57-B239-D2D46922AA6C`。
- **系统负载**：测量期间其他进程占满 CPU，`uptime` 显示 load average 为 **461～723**。所有耗时数据都应视为高负载下的保守上界；P99 的离散主要来自调度抖动。
- libopus 1.6.1（随仓库提供），NEON 已开启：`OPUS_ARM_PRESUME_NEON_INTR`。core 基准为 CMake Release（`-O3`）；Apple 测试为 Xcode Debug（Swift `-Onone`），链接 Release 版 `Opus.xcframework`。
- 工具：`ffmpeg` / `ffprobe`（Homebrew）、`whisper-cli`（whisper.cpp，`server/whisper/models/ggml-base.bin`）。

## 2. 输入与测量方法

| 实验 | 输入 | 测量 |
| --- | --- | --- |
| 编码 RTF 与链路占比 | 30 秒合成信号（220 Hz + 1.8 kHz，每 3 秒中 1 秒静音），16 kHz（纯编码）与 48 kHz（会话 + 编码），`N=20` | 墙钟耗时 / 音频时长 |
| App 链路占比 | 30 秒 48 kHz 440 Hz 正弦，每帧 1,024 样本，经 `RecordingPipeline` → `OggOpusFileWriter`，`N=20` | `coreProcessingNanoseconds`，计时范围包含编码与写文件 |
| 互通与转写 | macOS `say` 合成 12.95 秒英文语音，转为 16 kHz 单声道 S16LE（207,178 帧） | `ffprobe`、FFmpeg 解码样本数、Whisper 文本 |
| 原生解码时长 | 3 秒 16 kHz 440 Hz 正弦（正好 150 个包） | `AVAudioFile.length`、`AVAudioPlayer.duration` |
| 波形 SNR | 2 秒 16 kHz 多频信号（300/700/1300/2100 Hz）与 440 Hz 单频；VOIP / AUDIO × 24/32/48 kbps | 在 lookahead ±40 样本内搜索最佳对齐，计算 SNR |

## 3. 复现命令与原始数据路径

```sh
make test-core                    # 8 个 CTest 套件
make bench-core                   # 追加到 output/p2-2-opus-bench.jsonl
make test-apple-bridge            # 9 个 CTest 套件（core 8 + 桥接 1）
make test-apple-macos             # 日志 output/p2-2-macos-xctest.log
IOS_SIMULATOR_ID=9AB1A395-5D81-4D57-B239-D2D46922AA6C make test-apple-ios   # 日志 output/p2-2-ios-xctest.log
```

互通实验的输入、产物和转写文本在 `output/p2-2-interop/`（`speech.pcm`、`speech.ogg`、`speech.m4a`、`transcript-*.txt`、`encode-stats.json`）；SNR 探针的源码和输出为 `output/p2-2-interop/snr-probe.{cpp,txt}`。互通实验的命令：

```sh
ffmpeg -i speech.aiff -ac 1 -ar 16000 -f s16le speech.pcm
core/build/ogg_opus_encode --rate 16000 --channels 1 --bitrate 24000 --serial 4242 < speech.pcm > speech.ogg
ffmpeg -f s16le -ar 16000 -ac 1 -i speech.pcm -c:a aac -b:a 32k speech.m4a
ffprobe -show_entries stream=codec_name,duration_ts:format=duration speech.ogg
ffmpeg -i speech.ogg -ar 48000 -f s16le decoded-48k.pcm
ffmpeg -i speech.<ext> -ar 16000 -ac 1 -c:a pcm_s16le in.wav && whisper-cli -m ggml-base.bin -f in.wav -nt -otxt
```

## 4. 测量数据汇总

**编码耗时**（`output/p2-2-opus-bench.jsonl`，Release）：

| 管线 | P50 | P95 | P99 | P95 占比 |
| --- | --- | --- | --- | --- |
| `ogg_opus_encode`（30 秒 16 kHz） | 219.96 ms | 516.87 ms | 1,029.68 ms | 1.72%（RTF 0.017） |
| `audio_session_then_ogg_opus`（30 秒 48 kHz → 16 kHz → Ogg） | 255.05 ms | 336.18 ms | 398.74 ms | 1.12% |
| `audio_session`（对照） | 27.52 ms | 49.76 ms | 142.32 ms | 0.17% |

**App 链路**（Debug，会话 + 编码 + 写文件）：

| 平台 | core 耗时 P50 / P95 / P99 | 占比 P50 / P95 / P99 | 输出 |
| --- | --- | --- | --- |
| macOS | 828.50 / 1,850.18 / 3,378.62 ms | 2.76% / 6.17% / 11.26% | 30,000 ms，55,803 B |
| iOS Simulator | 712.36 / 818.75 / 835.33 ms | 2.37% / 2.73% / 2.78% | 30,000 ms，55,803 B |

**互通**（12.95 秒语音）：

- 编码输出 648 个包，共 37,570 B（约 23.2 kbps）；AAC 32 kbps 为 54,134 B。
- `ffprobe`：`codec_name=opus`，`duration=12.955125` s，比输入（12.948625 s）多 6.5 ms，正好是一个 pre-skip（312 / 48000）。ffprobe 的 `duration_ts=621846` 为原始 granule，没有扣除 pre-skip。
- FFmpeg 解码：621,534 帧（48 kHz），与 207,178 × 3 **完全相等**，说明 pre-skip 和末页裁尾都已生效。
- Whisper（base）：Ogg 与 m4a 的转写文本**完全相同**——“Good morning everyone, today we will review the recording paper line, the upload service, and the transcription worker. Please send your action items to the team before Friday.”（“pipeline”被识别为“paper line”是 base 模型的共同错误，与格式无关。）

**原生解码**（3 秒，150 个包）：macOS 与 iOS Simulator 上 `AVAudioFile` 和 `AVAudioPlayer` 都报告 `3.0` s，偏差为 0；文件 8,992 B。

**波形 SNR**（2 秒，最佳对齐偏移）：

| 模式 | 24 kbps 多频 | 32 kbps 多频 | 48 kbps 多频 | 24 kbps 440 Hz |
| --- | --- | --- | --- | --- |
| VOIP（SILK） | 13.13 dB @104 | 13.72 dB @104 | 14.06 dB @104 | 12.35 dB @104（最佳 27.12 dB @139） |
| AUDIO（CELT） | 18.09 dB @104 | 21.10 dB @104 | 25.78 dB @104 | 37.29 dB @104 |

## 5. 结论与发现

1. **B3 满足**：编码 RTF P95 为 0.017（远小于 1）；会话加编码的占比 P95 在 Release 下为 1.12%，App Debug 链路为 2.73%～6.17%，均低于 20%。这些数据是在 load average 400～700 的高负载下测得的。
2. **B4 满足**：FFmpeg 能识别并解码到逐样本一致的长度；ffprobe 时长偏差 6.5 ms（≤ 20 ms）；24 kbps Opus 与 32 kbps AAC 的 Whisper 文本相同，体积小 31%。
3. **B5 满足**（模拟器与 macOS）：`AVAudioFile` / `AVAudioPlayer` 都按末页 granule 裁尾，时长与采集时长一致。
4. **SILK 不保真波形**：VOIP 模式下最佳对齐偏移正好等于 lookahead（104 样本），排除了对齐错误；提高码率也只到 14 dB。单频信号的最佳偏移为 139，说明 SILK 的相位重建与原始信号不一致，而 AUDIO 模式没有这个现象。用户确认保持 VOIP，B2 的 SNR 只作回归护栏（≥ 12 dB），语音质量以 Whisper 对比为准。
5. Debug 链路占比由 P2-1 的 0.99% 升至 2.7%～6.2%：计时口径改为包含编码，且测量时负载更高，两行数据不能直接比较。
6. **B7 满足**（2026-10-22 22:09 补跑）：`AUDIO_FORMAT=ogg make smoke-asr` 用 core 工具生成 2785 ms、140 包、8307 字节的 Ogg Opus 样本。状态 `uploaded`（第 1 次轮询）→ `done`（第 2 次），转写为 “This is a test meeting transcription.”，Worker `asr_ms=287`。数据库行：`object_key=recordings/<id>/audio.ogg`、`content_type=audio/ogg`、`size_bytes=8307`。确认上传与 Worker 日志共享同一个 `request_id`。原始输出：`output/p2-2-b7-smoke.log`。

## 6. 下一步

- P2-3 在 iOS 真机上确认 Ogg Opus 的解码与回放，并在低负载环境下重测 App 链路占比。
- 如果后续出现转写质量差异，以 Whisper 对比为准，评估 32 kbps。
