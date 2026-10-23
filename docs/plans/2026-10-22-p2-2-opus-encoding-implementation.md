# P2-2 技术方案：core 内 libopus 编码与 Ogg Opus 落盘

- 日期：2026-10-22
- 需求：[P2 音频管道需求](../requirements/2026-10-22-p2-audio-pipeline-requirements.md) 中的 P2-2
- 状态：已实施，待用户终审；实施中的修正见文末「实施修正」（评审记录：[2026-10-22-p2-2-opus-review.md](../reviews/2026-10-22-p2-2-opus-review.md)，结论“不通过”，修订内容见文末「评审修订说明」）

## 1. 目标与验收

用 core 内的 libopus 替换 Apple 原生 AAC 编码：录音落盘、上传和转写全部改用 Ogg Opus。验收标准见需求文档「P2-2 验收标准」（B1 ～ B7）。

## 2. 容器格式调研与决定

### 2.1 实测证据

环境：Apple M1 Pro、macOS 27.0、Xcode 27.1 RC、iPhone 17 Pro Simulator（iOS 26）、FFmpeg 8.1（Homebrew）。原始命令和输出位于 `/tmp/opusprobe`（本次会话的临时目录），关键结论如下。

| 检查项 | Ogg Opus（`ffmpeg -c:a libopus`） | CAF Opus（`afconvert -f caff -d opus`） |
| --- | --- | --- |
| macOS `AVAudioFile` / `AVAudioPlayer` 读取 | 通过（181,804 帧，3.79 s） | 通过 |
| iOS 26 Simulator `AVAudioFile` / `AVAudioPlayer`（`swiftc -target arm64-apple-ios26.0-simulator` 后用 `simctl spawn` 运行） | 通过 | 通过 |
| FFmpeg 8.1 解码（Whisper `--convert` 依赖它） | 通过 | 通过 |
| FFmpeg 8.1 封装写出 | 通过 | **失败**：`[caf] muxing codec currently unsupported` |
| 扩展名为 `.audio` 时 FFmpeg 能否识别（Worker 临时文件就叫这个名字） | 通过（按内容识别） | 未测 |
| Android 原生播放（P4） | `MediaPlayer` / ExoPlayer 原生支持（Android 官方媒体格式表） | 无原生支持 |
| 规范 | RFC 3533（Ogg）+ RFC 7845（Ogg 封装 Opus），公开且简单 | Apple CAF 规范，单一厂商 |

### 2.2 决定

采用 **Ogg Opus**（`audio/ogg`，`.ogg`），封装逻辑由 core 生成**内存中的字节**，文件写入留在壳层：

- Apple 两端、FFmpeg、Whisper 和 Android 都能原生读取，只需维护一份格式。
- Ogg 页封装是纯计算（分段、CRC32、granule 位置），符合 core 的模块边界；P4 的 Android 可以直接复用。
- 不选 CAF：FFmpeg 不能写 CAF Opus，P2-4 服务端转码后无法回写同格式；Android 也读不了。

## 3. 依赖：libopus 1.6.1 源码随仓库提供

- 来源：`https://downloads.xiph.org/releases/opus/opus-1.6.1.tar.gz`，SHA256 为 `6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1`，与官方 `SHA256SUMS.txt` 核对一致。许可证为 BSD-3-Clause。
- 放到 `core/third_party/opus/`，排除 `dnn/`（22 MB，深度 PLC / DRED 模型）、`tests/`、`doc/` 和 meson 文件，体积从 29 MB 降到 6 MB。已实测裁剪后用 CMake 可以完整构建。
- 新增 `core/third_party/README.md`，记录版本、来源、校验和、裁剪清单和升级步骤。
- 构建选项：`OPUS_BUILD_TESTING=OFF`、`OPUS_BUILD_PROGRAMS=OFF`，关闭 pkg-config 和 CMake config 安装，DRED / OSCE / DEEP_PLC 保持默认关闭。
- 保留的许可证文件：`COPYING`、`AUTHORS`、`README`、`package_version`；`core/third_party/README.md` 中列出这份清单。

### 3.0 接入 core 的 CMake（评审 B-2、B-3、I-5）

```cmake
enable_language(C)
set(_jk_build_testing ${BUILD_TESTING})
set(BUILD_TESTING OFF)                       # libopus 用 OPUS_BUILD_TESTING OR BUILD_TESTING 决定是否编译 tests/
set(OPUS_BUILD_TESTING OFF CACHE BOOL "" FORCE)
set(OPUS_BUILD_PROGRAMS OFF CACHE BOOL "" FORCE)
set(OPUS_INSTALL_PKG_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
set(OPUS_INSTALL_CMAKE_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
if(APPLE AND CMAKE_SYSTEM_PROCESSOR STREQUAL "arm64")
    set(OPUS_PRESUME_NEON ON CACHE BOOL "" FORCE)
endif()
add_subdirectory(third_party/opus EXCLUDE_FROM_ALL)
set(BUILD_TESTING ${_jk_build_testing})
target_link_libraries(jkmeeting_core PUBLIC opus)   # 静态库的 PRIVATE 依赖不会传到最终链接
```

公开 C++ 头文件仍不包含 `opus.h`；opus 的 include 路径传给测试目标是可以接受的，需要直接调用 `opus_decode` 的测试因此可以用。验证要走真实路径：`make test-core` 和 `make test-apple-bridge`（父工程开着 `include(CTest)`），不能只靠单独配置 opus 的结果。

### 3.1 NEON 配置（已实测）

- macOS arm64 下 `CMAKE_SYSTEM_PROCESSOR=arm64`，不匹配 libopus 判断用的 `aarch64`，所以默认不假定 CPU 有 NEON，还会打出一条不中断构建的 `ERROR` 消息。需要显式传 `-DOPUS_PRESUME_NEON=ON`；修正后编译参数包含 `OPUS_ARM_PRESUME_NEON_INTR`。
- iOS 交叉编译时 `CMAKE_SYSTEM_PROCESSOR` 为空，libopus 认不出 ARM，NEON 被完全关闭。需要传 `-DCMAKE_SYSTEM_PROCESSOR=aarch64`；修正后真机和模拟器两个目标都带 `OPUS_ARM_PRESUME_NEON_INTR`。

### 3.2 Apple 构建方式：预编译 `Opus.xcframework`

- 新增 `apple/scripts/build-opus-xcframework.sh`，用 CMake 分别构建 `macos-arm64`、`ios-arm64`、`ios-arm64-simulator` 三个静态库，再用 `xcodebuild -create-xcframework` 合成 `apple/third_party/Opus.xcframework`（已实测，2.8 MB）。产物不入库（已加入 `.gitignore`），用版本戳文件判断是否需要重建。
- 新增 `make opus-xcframework`；`test-apple-ios` 和 `test-apple-macos` 依赖它。
- `project.yml` 中，两个 Shared target、两个 App target 和两个 Tests target 都声明 `- framework: third_party/Opus.xcframework`（`embed: false`），不依赖 Xcode 自动传递静态库依赖（评审 B-3）。实施时要拿 `xcodebuild` 链接成功的日志作为证据。
- 版本戳文件的内容包括 opus 版本、`OPUS_PRESUME_NEON`、`CMAKE_SYSTEM_PROCESSOR` 和 SDK 名称，任何一项变化都会触发重建。`HEADER_SEARCH_PATHS` 增加 `$(SRCROOT)/../core/third_party/opus/include`，供 core 的 `.cpp` 使用。
- 不选“把 libopus 源码直接列进 XcodeGen”：libopus 的平台宏、SIMD 源文件分组和 `config.h` 由它自己的 CMake 生成，在 XcodeGen 里手工复刻很脆弱。xcframework 是 Apple 官方的多平台预编译二进制格式，而 P4 的 Android 可以直接 `add_subdirectory` 同一份 CMake。
- 只构建 arm64；Intel Mac 和 x86_64 模拟器不在范围内（本机为 Apple Silicon）。
- `apple/CMakeLists.txt`（桥接测试）通过 `add_subdirectory(core)` 自动获得 opus，不需要 xcframework。

## 4. core 设计

core 的公开头文件不暴露 `opus.h`，实现用 pimpl 隐藏 libopus 类型。新增模块如下。

### 4.1 `jkmeeting/opus_encoder.h`：`OpusPacketEncoder`

```cpp
struct OpusEncoderConfig {
    int sample_rate = 16'000;   // 仅 8k/12k/16k/24k/48k
    int channels = 1;           // 1 或 2
    int bitrate_bps = 24'000;   // VBR 目标码率
    int frame_ms = 20;          // 仅 10/20/40/60
};
struct EncodedPacket { std::vector<uint8_t> data; int64_t frames = 0; };  // frames 以输入采样率计

class OpusPacketEncoder {
public:
    explicit OpusPacketEncoder(const OpusEncoderConfig&);
    ~OpusPacketEncoder();
    bool valid() const;
    bool encode(const int16_t* samples, std::size_t sample_count, std::vector<EncodedPacket>& out);
    bool flush(std::vector<EncodedPacket>& out);   // 末帧补零编码
    int pre_skip_48k() const;                       // OPUS_GET_LOOKAHEAD 换算到 48 kHz
    int64_t input_frames() const;
};
```

- 单位约定（评审 I-2，与 ADR 0002 一致）：`sample_count` 是交错后的总采样值数量，必须能被 `channels` 整除；`EncodedPacket.frames` 和 `input_frames()` 是每声道帧数；granule 按 48 kHz 每声道计数，立体声不加倍（RFC 7845 §4）。
- 应用模式为 `OPUS_APPLICATION_VOIP`，信号类型提示为 `OPUS_SIGNAL_VOICE`，开启 VBR，复杂度沿用 libopus 默认值。测试锁定这套生产配置，不为了通过 SNR 测试而改用 `AUDIO` 模式。
- 编码器不是线程安全的；壳层只在 `PipelineStore` 的串行队列上使用它。
- 内部缓冲不足一帧的样本；每满一帧调用一次 `opus_encode`，单包上限 1,275 字节（RFC 6716）。
- `opus_encode` 返回负值时，`encode` 返回 `false`，之后的调用一律失败（错误不可恢复）。

### 4.2 `jkmeeting/ogg_opus_muxer.h`：`OggOpusMuxer`（纯字节生成）

```cpp
class OggOpusMuxer {
public:
    OggOpusMuxer(uint32_t serial, int channels, int input_sample_rate, int pre_skip_48k);
    std::vector<uint8_t> headers();                                  // OpusHead 页（BOS）+ OpusTags 页
    std::vector<uint8_t> append(const EncodedPacket&, int64_t granule_end_48k);  // 返回已封好的页
    std::vector<uint8_t> finish(int64_t final_granule_48k);          // 输出剩余包并设置 EOS
};
```

- `OpusHead` 共 19 字节：版本 1、声道数、`pre_skip`、原始采样率、输出增益 0、映射族 0。`OpusTags` 为 `"OpusTags"` + uint32 LE vendor 长度 + vendor 字符串（取 `opus_get_version_string()`）+ uint32 LE `0`（不写用户注释）。所有多字节字段都是小端序（RFC 7845 §5.1、§5.2）。
- 两个头各占一页，granule 为 0（RFC 7845 §3）。`headers()` 只允许调用一次，再次调用返回空。
- 组页算法（评审 B-1）：`append` 只把包追加到**尚未封口**的当前页。只有在“再加入这个包会超过 50 个包，或超过 255 个 lacing 值”时，才先把当前页（不带 EOS）写出，再开新页。`finish` 把当前页（含最后若干个包）加上 EOS 标志和 `final_granule` 一次性写出。禁止用空的 EOS 页补救：空页没有完整包，granule 只能是 −1，RFC 7845 §4.4 的裁尾无法成立。没有任何音频包时（零长度录音），`finish` 返回空，由壳层判定为无效文件。
- lacing（评审 I-1）：包长为 `n` 字节时，写 `floor(n/255)` 个 255，再写一个 `n % 255`（可以是 0）；1275 字节的包占 6 个 lacing 值。
- 音频页的 granule 等于页内最后一个完整包结束时**累计解码出的样本数**，即 `累计帧数 × 48000 / input_rate`。这个数已经包含被 pre-skip 丢弃的样本，不能再加一次 `pre_skip`（RFC 7845 §4：PCM 位置 = granule − pre_skip；与 `opusenc` 的实现一致）。Opus 的合法采样率都能整除 48000，逐包累加不会产生舍入误差。
- 末页的 `final_granule = pre_skip + input_frames × 48000 / input_rate`，它小于最后一个包解码出的样本数。解码器据此裁掉末帧补的零，使播放时长与采集时长一致（RFC 7845 §4.4）。muxer 校验 `上一页 granule ≤ final_granule ≤ 最后一个包的结束位置`，否则 `finish` 失败。
- 因此 `flush` 必须继续补零编码，直到 `已编码帧数 ≥ input_frames + lookahead`；否则最后 lookahead 个真实样本没有被编出来，`final_granule` 会超过实际解码长度。lookahead 小于一帧，所以最多补两帧，并且保证 `final_granule` 大于上一页的 granule。
- CRC：多项式 `0x04C11DB7`，初值 0，不反射，不异或；计算时 CRC 字段先置零。用 `"123456789"` 的已知结果 `0x89A1897F` 做测试。
- `serial` 由壳层传入（Swift 用 `UInt32.random(in: 1...UInt32.max)`，排除 0），这样 core 的输出是确定的，便于测试。

### 4.3 `jkmeeting/ogg_opus_encoder.h`：`OggOpusEncoder`（组合）

- 组合 `OpusPacketEncoder` 和 `OggOpusMuxer`，接口为 `headers()`、`encode(pcm) -> bytes`、`finish() -> bytes`，以及 `duration_ms()`、`packet_count()`、`bytes_written()`。
- `duration_ms` 由输入样本数推导，作为上传元数据使用。

## 5. 桥接与 Swift

- `JKMeetingCore.h/.mm` 新增 `JKMeetingOggOpusEncoder`：
  - `-initWithSampleRate:channels:bitrate:serial:error:`
  - `-headerData`
  - `-encodePCMData:error:`，返回 `NSData`，可能为空
  - `-finishWithError:`
  - 只读属性 `durationMs`、`packetCount`
- 新增 `apple/Shared/Recording/OggOpusFileWriter.swift`，实现 `ProcessedAudioFrameWriter`（评审 I-6）：
  - `init`：先创建文件，再用 `FileHandle(forWritingTo:)` 打开，并用 `try write(contentsOf:)` 写入头部。
  - `append`：校验采样率和声道数后编码；返回的字节非空时才用 `try write(contentsOf:)` 写入。
  - `finish`：写入尾页后 `try synchronize()`、`try close()`，再用 `AVAudioFile(forReading:)` 校验可读、长度大于 0，返回 `RecordingMetadata`（`durationMs` 来自编码器输入样本数，不依赖解码器的时长）。再次调用 `finish` 抛出 `alreadyFinished`，与 `AppleAudioFileWriter` 一致。
  - `discard`：关闭并删除文件。
  - 所有写入都使用会抛错的 API；不使用不抛错的 `write(_ data:)`，它失败时可能只留下半个文件。
- `AppleRecordingManager`：文件扩展名改为 `.ogg`，写入器换成 `OggOpusFileWriter`。`AppleAudioFileWriter` 保留，现有测试还在使用，主链路不再引用。
- `RecordingAPIClient`：Content-Type 和上传路径校验改为按 `metadata.fileURL.pathExtension` 映射（`ogg → audio/ogg`、`m4a → audio/mp4`），对象路径校验为 `/audio.<ext>`。扩展名未知时直接抛错，不默认当作 m4a（评审 I-6）。
- `AppleRecordingManagerTests` 切换写入器后会真正走编码路径（模拟采集 0.5 秒，共 25 个包），也作为 B5 的补充证据。
- `AppleAudioPlayback` 不改：实测 `AVAudioPlayer` 能直接播放 Ogg Opus。

## 6. 服务端

- `recording_handler.go`：把 `content_type` 改为**精确匹配**的白名单 `{"audio/mp4": "m4a", "audio/ogg": "ogg"}`（不接受 `codecs=` 等参数），对象键为 `recordings/<id>/audio.<ext>`；其他值返回 `400`。这收紧了原先“任意非空字符串都接受”的行为。App、`loadbench` 默认参数、`smoke.sh`、`smoke-asr.sh` 和 `measure-p0-baselines.py` 都使用 `audio/mp4`，不受影响。
- `server/scripts/p1-experiments.py`（评审 I-4）：该脚本通过 HTTP 创建记录时用 `audio/mp4-p1-bench` 作为实验标签，白名单生效后会返回 400。修改如下：
  - HTTP 请求改用 `audio/mp4`。
  - 清理时用“本次实验开始时间之后创建，且 `status='created'`、`duration_ms=30000`、`size_bytes=120000`、`content_type='audio/mp4'`”精确匹配压测行。压测行从未上传，真实录音不会同时满足这些条件。
  - 直接用 SQL 插入的 `audio/mp4-p1-1m` / `audio/mp4-p1-backlog` 不经过 API，标签和清理方式都不变。
  - 本次只做语法检查（`python3 -m py_compile`），不重跑 P1 实验；交付时说明。
- 数据库：不改 schema（`content_type` 列已经存在）。
- Worker 和 ASR：不改。Whisper 的 `--convert` 通过 FFmpeg 按内容识别格式，已实测 `.audio` 扩展名下的 Ogg 也能正确解码。
- `server/scripts/smoke-asr.sh`：新增 `AUDIO_FORMAT=ogg` 选项，**默认仍为 m4a**。开启后使用 core 工具 `ogg_opus_encode` 生成样本（同时覆盖我们自己的封装器），上传时使用 `audio/ogg`，用于 B7 端到端验证。

## 7. 错误处理

| 情况 | 处理 |
| --- | --- |
| 编码配置非法（采样率、帧长、声道数） | `valid() == false`；桥接层返回 `InvalidInput`；Swift 写入器的 `init` 抛错，录音启动失败（沿用现有 `.invalidRecordedFile` 路径） |
| `opus_encode` 失败 | 返回 `false` 后会话不可恢复；桥接层返回 `ProcessingFailed`；`RecordingPipeline` 丢弃写入器 |
| 写文件失败 | 沿用 `FileHandle` 抛出的错误；`RecordingPipeline` 丢弃写入器并删除文件 |
| 服务端收到未知 `content_type` | 返回 `400`，不创建记录 |

## 8. 测试策略（TDD）

- `core/tests/ogg_opus_muxer_test.cpp`：CRC 已知值；用测试内的页解析器校验：首页为 BOS、`OpusHead` 各字段正确、`OpusTags` 的二进制布局正确且单独一页、页序号连续、granule 单调、末页 EOS 且 granule 正确，并对每一页重算 CRC。还要覆盖：
  - 包长 255、510、600、1275 字节时的 lacing（含结尾的 0），且解析后包内容与原始字节一致；
  - **正好 50 个包、正好 150 个包、50 个包加 1 个补零包**这三种情况下，最后一个包都在 EOS 页上，且没有空页；
  - 零个包时 `finish` 返回空；`headers()` 第二次调用返回空。
- `core/tests/opus_encoder_test.cpp`：1 秒 16 kHz 正弦编码后包数为 50；用 libopus 解码器回放，裁掉 `pre_skip` 后长度与输入一致；对齐后的语音频段多频信号波形 SNR ≥ 12 dB（生产配置 VOIP / 24 kbps）。实施时实测：VOIP 下为 13.1 dB（48 kbps 时也只有 14.1 dB），AUDIO 下为 18.1 dB；最佳对齐偏移正好等于 lookahead，排除了对齐错误。SILK 是参数化编码，不保真波形，经用户确认（2026-10-22）保持 VOIP，SNR 降为回归护栏，语音质量以 B4 的 whisper 对比为准；立体声的 `frames` 是每声道帧数，granule 不加倍；非法配置会被拒绝；`flush` 之后再编码失败。
- `core/tests/ogg_opus_encoder_test.cpp`：组合输出的字节可以被上述页解析器完整解析；`final_granule` 和时长计算正确；任意分块方式不影响输出（输出逐字节相同）。
- `core/tools/ogg_opus_encode.cpp`：命令行工具，从 stdin 读取 S16LE PCM，向 stdout 输出 `.ogg`，用于外部互通验证和测量（文件 IO 只发生在工具里，不进入 core 库）。
- `core/bench/audio_session_bench.cpp`（评审 I-3）：新增两条记录：`ogg_opus_encode`（30 秒 16 kHz PCM 编码，报告 RTF）和 `audio_session_then_ogg_opus`（48 kHz 会话输出后接编码，报告总耗时占比）。均为 `N=20`，同时报告 P50/P95/P99。`make bench-core` 的输出改写到 `output/p2-2-opus-bench.jsonl`，P2-1 的旧文件不再追加。
- `RecordingPipeline` 的 `coreProcessingNanoseconds` 改为同时统计写入器中的编码耗时（计时范围包含 `writer.append`）。App 链路的占比会因此包含 AAC 或 Opus 的写入耗时，这一变化会写进指标说明。
- Apple XCTest（`apple/SharedTests/OggOpusFileWriterTests.swift`，iOS 与 macOS 共用）：写出 3 秒正弦（正好 150 个包，覆盖 EOS 边界）后，**分别**断言 `AVAudioFile.length` 和 `AVAudioPlayer.duration` 与预期相差在 20 ms 以内；超出时把实际值写进报告，不为迁就解码器而修改 granule。另外验证 `discard` 会删除文件、格式不匹配时抛错、二次 `finish` 抛错。
- `RecordingAPIClientTests`（评审 I-8）：保留 m4a 用例，新增 `.ogg` 用例（创建请求体中的 `content_type` 为 `audio/ogg`、PUT 的 Content-Type 为 `audio/ogg`、预签名路径为 `/audio.ogg`），以及扩展名未知的负向用例。
- 桥接测试：编码器的创建、编码、结束，以及非法参数。
- 服务端：`recording_handler_test.go` 增加 `audio/ogg` 用例（对象键以 `audio.ogg` 结尾）和未知类型返回 `400` 的用例。
- 互通验证（结果写进实验报告）：工具输出依次经过 `ffprobe`（应识别为 `opus`，`duration` 偏差 ≤ 20 ms）、`whisper-cli`（同一段 `say` 语音分别编成 m4a（AAC 32 kbps）和 Ogg Opus（24 kbps），比较转写文本）、`AUDIO_FORMAT=ogg make smoke-asr`（状态进入 `done`）。

## 9. 文件清单

| 操作 | 路径 |
| --- | --- |
| 新增 | `core/third_party/opus/`（libopus 1.6.1 裁剪版）、`core/third_party/README.md` |
| 新增 | `core/include/jkmeeting/{opus_encoder,ogg_opus_muxer,ogg_opus_encoder}.h`、`core/src/` 下对应的 `.cpp` |
| 新增 | `core/tests/{opus_encoder,ogg_opus_muxer,ogg_opus_encoder}_test.cpp`、`core/tests/ogg_page_parser.h`、`core/tools/ogg_opus_encode.cpp` |
| 修改 | `core/CMakeLists.txt`、`core/bench/audio_session_bench.cpp`、`core/README.md` |
| 新增 | `apple/scripts/build-opus-xcframework.sh`、`apple/SharedTests/OggOpusFileWriterTests.swift`、`apple/Shared/Recording/OggOpusFileWriter.swift` |
| 修改 | `apple/project.yml`、`apple/Bridge/JKMeetingCore.{h,mm}`、`apple/BridgeTests/JKMeetingCoreBridgeTests.mm`、`apple/Shared/Recording/{AppleRecordingManager,RecordingAPIClient}.swift` 及相关测试 |
| 修改 | `server/internal/httpapi/recording_handler{,_test}.go`、`server/scripts/smoke-asr.sh`、`server/scripts/p1-experiments.py` |
| 修改 | `apple/Shared/Recording/RecordingPipeline.swift`（计时包含编码）、`apple/SharedTests/RecordingAPIClientTests.swift` |
| 修改 | `Makefile`（`opus-xcframework`，以及 Apple 测试目标的依赖）、`.gitignore` |
| 新增 | `docs/decisions/0006-opus-ogg-container.md`、`docs/reports/2026-10-22-p2-2-opus-report.md` |
| 修改 | `docs/metrics.md`、需求文档状态 |

## 10. 实施步骤

1. 放入 libopus 源码并接入 CMake，确认 `make test-core` 仍然通过。
2. 按 TDD 依次实现 muxer（CRC 与页结构）、编码器（含解码回放）和组合编码器。
3. 实现工具和基准；运行 `make bench-core`，并完成 `ffprobe` / `whisper-cli` 互通验证。
4. 编写 xcframework 脚本和 Makefile 目标，修改 `project.yml`；完成桥接与桥接测试（`make test-apple-bridge`）。
5. 实现 Swift 写入器并切换主链路；运行 `make test-apple-macos` 和 `make test-apple-ios`。
6. 修改服务端白名单及测试（`make test-server`），然后运行 `AUDIO_FORMAT=ogg make smoke-asr`（依赖本机容器和 Whisper；若环境未启动，交付时说明）。
7. 编写 ADR 0006、实验报告，更新指标台账。

## 11. 风险

- 模拟器验证不等于真机：iOS 真机解码 Ogg Opus 的行为还要在 P2-3 用真机确认。
- 低码率可能影响转写准确率：24 kbps 只是先验值，以 `whisper-cli` 的对比结果为准；如果出现差异，改为 32 kbps 并记录。
- xcframework 需要预先构建，第一次运行 Apple 测试会多花约 30 秒；版本戳文件保证不会重复构建。
- 旧的 m4a 录音和服务端已有对象不迁移，回放和转写路径都同时兼容两种格式。

## 评审修订说明

独立评审只做了一轮，结论为“不通过”：3 个阻塞问题、8 个重要问题、13 条建议。以下逐项说明处理结果（正文已同步修改）。

| 评审项 | 处理 | 正文位置 |
| --- | --- | --- |
| B-1 满 50 包就封页会丢失 EOS | 采纳：当前页延迟封口，下一个包放不下时才写出；`finish` 把最后若干个包和 EOS 一起写出；禁止空 EOS 页；新增 50 / 150 / 50+1 个包的边界测试 | §4.2、§8 |
| B-2 父工程的 `BUILD_TESTING` 会强制编译已删除的 opus `tests/` | 采纳：`add_subdirectory` 前临时关闭 `BUILD_TESTING`，之后恢复，并用 CACHE FORCE 设置 opus 选项；用 `make test-core` / `make test-apple-bridge` 的真实路径验证。已核实 libopus `CMakeLists.txt:36` 为 `if(OPUS_BUILD_TESTING OR BUILD_TESTING)` | §3.0 |
| B-3 静态库没有把 libopus 传到最终链接 | 采纳：`target_link_libraries(jkmeeting_core PUBLIC opus)`；XcodeGen 的 Shared、App、Tests 三类 target 都声明 xcframework；以链接日志为证据 | §3.0、§3.2 |
| I-1 包长为 255 倍数时需要以 lacing 0 结尾 | 采纳：给出 lacing 公式，补充 255 / 510 / 1275 字节的测试 | §4.2、§8 |
| I-2 `sample_count` / `frames` 的单位 | 采纳：与 ADR 0002 一致，granule 按每声道计数；补充立体声测试 | §4.1、§4.2 |
| I-3 B3 的“会话加编码”占比没有测量 | 采纳：bench 新增两条记录并写入新的输出文件；App 链路计时包含编码 | §8 |
| I-4 白名单会打断 `p1-experiments.py` | 采纳并核实（脚本第 147/194/246 行）：HTTP 请求改用 `audio/mp4`，清理改为按时间窗口加压测行特征精确匹配；只做语法检查，不重跑 P1 实验 | §6 |
| I-5 CMake 路径要显式开启 NEON | 采纳：在 core 的 CMake 里设置 `OPUS_PRESUME_NEON` | §3.0 |
| I-6 `FileHandle` 写入要能抛错；未知扩展名不能默认 m4a | 采纳 | §5 |
| I-7 保留许可证文件 | 采纳 | §3 |
| I-8 客户端缺少 ogg 上传测试 | 采纳 | §8 |
| 建议 1（Xcode 静态库依赖是否自动传递，待确认） | 已并入 B-3，显式声明，不依赖自动传递 | §3.2 |
| 建议 2（`smoke-asr` 默认仍为 m4a） | 采纳 | §6 |
| 建议 3（VOIP 下纯正弦的 SNR 可能不稳定） | 采纳：锁定生产配置；必要时改用多频信号，并写进报告 | §8 |
| 建议 4（AVAudioFile 是否按 granule 裁尾，待确认） | 采纳：分别断言，超差时如实记录 | §8 |
| 建议 5（VBR 下的文件体积） | 采纳：报告记录典型体积和峰值体积 | 实验报告 |
| 建议 6（Whisper 兼容性） | 与方案一致，无需修改 | — |
| 建议 7（线程安全） | 采纳：注明编码器只在串行队列上使用 | §4.1 |
| 建议 8（OpusTags 布局与小端序） | 采纳 | §4.2 |
| 建议 9（父工程启用 C 语言） | 采纳：`enable_language(C)` | §3.0 |
| 建议 10（serial 排除 0） | 采纳 | §4.2 |
| 建议 11（版本戳包含编译选项） | 采纳 | §3.2 |
| 建议 12（`headers()` 只调用一次） | 采纳 | §4.2 |
| 建议 13（管理器测试变成集成测试） | 采纳：作为 B5 的补充证据 | §5 |

## 实施修正

实施时发现并修正以下问题（正文已同步）：

| 问题 | 证据 | 处理 |
| --- | --- | --- |
| §4.2 原写法中，音频页 granule 多加了一次 `pre_skip` | RFC 7845 §4：PCM 位置 = granule − pre_skip；与 `opusenc` 的实现一致 | 音频页 granule 改为累计解码样本数，只有末页包含 `pre_skip`；`flush` 补零到覆盖 lookahead。FFmpeg 解码长度与输入逐样本一致 |
| B2 的 15 dB SNR 门限在 VOIP 模式下无法达到 | 最佳对齐偏移正好等于 lookahead，VOIP 24/32/48 kbps 分别为 13.1/13.7/14.1 dB，AUDIO 为 18.1 dB | 用户确认保持 VOIP，SNR 降为 ≥ 12 dB 的回归护栏，语音质量以 Whisper 对比为准（Ogg 与 AAC 文本相同） |
| libopus 把空构建类型改成 Release，使测试中的 `assert` 失效 | 全新构建后缓存中 `CMAKE_BUILD_TYPE=Release`，测试报 unused variable | core 在构建类型为空时默认用 Debug（见问题排查记录） |
| `OPUS_MAY_HAVE_NEON=OFF` 导致 NEON 内建函数未声明 | `celt_neon_intr.c` 编译报错 | 保持默认 `ON`，只设 `OPUS_PRESUME_NEON=ON`；那条 `ERROR...` 只是提示文本 |
| B7 首次交付时未运行 | 当时本机 Colima Docker 未启动 | 2026-10-22 22:09 补跑通过：`uploaded` → `done`，对象键为 `audio.ogg`（见实验报告结论 6） |
