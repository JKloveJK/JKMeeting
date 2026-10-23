# 0006：录音改为在 core 中编码 Opus，并由 core 自行封装为 Ogg

- 日期：2026-10-22
- 状态：已接受
- 关联：[P2 需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)、[P2-2 方案](../plans/2026-10-22-p2-2-opus-encoding-implementation.md)、[评审记录](../reviews/2026-10-22-p2-2-opus-review.md)、[实验报告](../reports/2026-10-22-p2-2-opus-report.md)、[ADR 0005](0005-core-streaming-audio-session.md)

## 背景

P0 / P2-1 的录音文件由 Apple 壳层通过 `AVAudioFile` 编码为 AAC `.m4a`。路线文档要求 P2 由 C++ core 负责编码，使 Android（P4）复用同一份实现，并且只允许引入 libopus 这一个第三方库。编码之后还要选容器：容器必须能被 Apple 原生播放（`AVAudioPlayer` / `AVAudioFile`），也要能被服务端 Whisper（经 FFmpeg `--convert`）识别。

## 决定

- **编码**：core 新增 `OpusPacketEncoder`，固定 `OPUS_APPLICATION_VOIP`、`OPUS_SIGNAL_VOICE`、VBR、24 kbps、20 ms 帧、16 kHz 单声道。`flush` 补零编码到 `已编码帧数 ≥ 输入帧数 + lookahead`。
- **容器**：选 Ogg Opus（RFC 7845），由 core 的 `OggOpusMuxer` 自行生成字节，不引入 libogg：
  - 当前页延迟封口，`finish` 输出含最后一个包的 EOS 页，不写空 EOS 页；
  - 音频页 granule 为累计解码样本数，末页为 `pre_skip + 输入帧数 × 48000 / 输入采样率`，由解码器裁掉补零。
- **组合**：`OggOpusEncoder` 只做字节生成，文件 IO 留在壳层（`OggOpusFileWriter` 使用会抛错的 `FileHandle` API）。core 仍不接触文件或网络。
- **依赖**：libopus 1.6.1 源码随仓库提供（`core/third_party/opus`，裁剪后 6 MB）。
  - CMake 用 `add_subdirectory` 接入。
  - Apple 端预编译为 `Opus.xcframework`（macos-arm64、ios-arm64、ios-arm64-simulator），在 XcodeGen 的六个 target 上显式声明。
- **服务端**：`content_type` 改为精确白名单 `audio/mp4 → audio.m4a`、`audio/ogg → audio.ogg`，其他值返回 400；schema、Worker 与 ASR 不变。
- **质量判据**（用户 2026-10-22 确认）：
  - SILK 是参数化编码，不保真波形，波形 SNR 只作回归护栏（≥ 12 dB）；
  - 语音质量以同一段语音的 Whisper 转写对比为准。

不选择：

- **CAF 容器**：Apple 原生支持，但 FFmpeg 不能写 CAF Opus，服务端和 Android 侧缺少对称工具链（调研实测见方案 §2.1）。
- **引入 libogg**：Ogg 页格式只有 CRC、lacing 和 granule 三个要点，约 150 行即可实现并充分测试；多一个依赖违背“P2 只引入 libopus”的约束。
- **`OPUS_APPLICATION_AUDIO`**：波形 SNR 更高（18.1 dB 对 13.1 dB），但 VOIP 对语音更合适，且转写结果与 AAC 一致，没有必要为了指标切换。
- **把 libopus 源文件直接列进 XcodeGen**：平台宏、SIMD 源文件分组和 `config.h` 由 libopus 自己的 CMake 生成，手工复刻很脆弱。

## 依据

- `make test-core`（8 个套件）：CRC 已知值、lacing（255/510/600/1275 字节）、50/51/150 个包时的 EOS 边界、granule 与裁尾、分块不变性（输出逐字节相同）、解码长度。
- 互通：FFmpeg 解码出的样本数与输入完全一致（621,534 = 207,178 × 3）；同一段 12.9 秒语音的 Whisper 转写文本与 AAC 32 kbps 完全相同，文件体积小 31%。
- `make bench-core`（Release）：30 秒编码 RTF P95 `0.017`；会话加编码占比 P95 `1.12%`（测量时系统负载很高，见实验报告）。

## 后果

- 新录音的扩展名为 `.ogg`。旧的 `.m4a` 录音和服务端已有对象不迁移，回放、上传和转写路径同时兼容两种格式。
- 客户端遇到未知扩展名时直接失败，新增格式必须同时修改客户端映射和服务端白名单。
- 第一次运行 Apple 测试需要构建 xcframework（版本戳一致时跳过）；升级 libopus 时须同步更新 `core/third_party/README.md`。
- iOS 真机解码 Ogg Opus 的行为在 P2-3 用真机确认；本次只在 macOS 与 iOS Simulator 上验证。
