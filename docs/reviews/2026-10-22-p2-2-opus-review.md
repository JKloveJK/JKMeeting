# P2-2 Opus 编码技术方案对抗评审

- 日期：2026-10-22
- 评审角色：独立评审专家（音视频编解码 + 跨端 C++ + Apple 构建 + Go 服务端）
- 关联需求：`docs/requirements/2026-10-22-p2-audio-pipeline-requirements.md`（P2-2 / B1～B7）
- 关联方案：`docs/plans/2026-10-22-p2-2-opus-encoding-implementation.md`
- 评审范围：需求覆盖、RFC 正确性、模块边界与所有权、构建可执行性、跨模块兼容、错误处理与可测试性
- 约束：只读代码，不修改源码；不参与方案编写或后续编码

## 结论

**不通过。**

容器选型（Ogg Opus）、core 只出内存字节、pimpl 隐藏 `opus.h`、旧 m4a 双轨兼容，以及 B1～B7 的主验收映射方向是对的。但按方案现稿实现，会出现三类实施前必须改掉的缺陷：

1. 整数秒录音很容易写出**没有 EOS、或 EOS 不在含末包页上**的 Ogg，B1 不成立。
2. 把裁掉 `tests/` 的 libopus 以 `add_subdirectory` 接入现有 `include(CTest)` 的 core CMake 时，**配置会失败**，`make test-core` 迈不过方案第 1 步。
3. `jkmeeting_core` 是静态库，方案未规定 libopus 如何传到最终可执行文件；按当前 CMake / XcodeGen 链接方式，新测试和桥接会在链接期失败。

架构可以保留。上述三项修订完成前不应编码。

---

## 阻塞问题

### B-1 满 50 包即封页会丢掉末页 EOS，B1 与 3 秒 Apple 测试会踩中

- **位置：** 方案 §4.2 `OggOpusMuxer::append` / `finish`；§5 `OggOpusFileWriter.finish`；§8 muxer 测试与 3 秒 XCTest
- **问题描述：** 组页策略写的是「页内累计约 1 秒的包（50 个 20 ms 包）……时封页」，`append` 返回已封好的页，`finish`「输出剩余包并设置 EOS」。20 ms 帧与 1 秒边界整除。录音长度为整数秒时，最后一包恰好把当前页填满并在 `append` 里写出；此时 `finish` 没有剩余包，已经落盘的末页也改不了。结果是：末页没有 EOS，或只能再写一页空 EOS。空 EOS 页没有完整包，granule 按 RFC 3533 应为 `-1`，RFC 7845 §4.4 的裁尾（用 EOS 页与上一完整包页的 granule 差）无法成立。
- **依据：**
  - RFC 7845 §3：「A logical stream ends with a page with the 'end of stream' flag set」。
  - RFC 7845 §4.4：EOS 页的 granule 可以小于该页全部包解出的样本数；保留样本数 = EOS 页 granule − 上一含完整包音频页的 granule；丢弃量不应超过**最后一包**解出的样本。因此最后一包必须落在 EOS 页上。
  - 方案 §8 的 Apple 用例是「写出 3 秒正弦」：3.000 s / 20 ms = 150 包 = 3×50，正好三次满页。
  - 壳层 `FileHandle` 写入后无法回改已返回的页字节（方案 §5）。
- **修改建议：** 写明标准 muxer 算法：`append` 只把包追加到**未封**的当前页；仅当「再放入下一包会超过 50 包或 255 个 lacing」时，先写出**上一页**（无 EOS）再开新页。`finish` 必须把当前页（含最后若干包）加上 EOS 和 `final_granule` 后一次性写出。禁止用空 EOS 页补救。单测至少覆盖：正好 50 包、正好 150 包（3 s）、50 包 + 1 个补零 flush 包。

### B-2 父工程 `BUILD_TESTING=ON` 会强制编译已删除的 opus `tests/`，CMake 配置失败

- **位置：** 方案 §3、§10 步骤 1；`core/CMakeLists.txt` 第 9 行；`apple/CMakeLists.txt` 第 9、13 行
- **问题描述：** 方案准备 `OPUS_BUILD_TESTING=OFF` 并删掉 `tests/`。这只在「单独配置 opus、且父级没有打开 `BUILD_TESTING`」时成立。core 与 Apple 桥接 CMake 都先 `include(CTest)`，CMake 会把 `BUILD_TESTING` 设为 ON。libopus 1.6.1 的逻辑是 `if(OPUS_BUILD_TESTING OR BUILD_TESTING)`，随后在 `tests/` 里 `add_executable(test_opus_decode ...)`。源码已被裁掉时，`cmake -S core -B core/build` 会在找测试源文件处失败。已知「裁剪后 CMake 可构建」只覆盖独立构建，不能外推到 `add_subdirectory`。
- **依据：**
  - `core/CMakeLists.txt:9`：`include(CTest)`。
  - `apple/CMakeLists.txt:9,13`：同样 `include(CTest)`，再 `add_subdirectory(../core)`。
  - libopus 1.6.1 `CMakeLists.txt`：`option(OPUS_BUILD_TESTING OFF)` 之后是 `if(OPUS_BUILD_TESTING OR BUILD_TESTING)`；约第 711 行起 `if(BUILD_TESTING AND NOT BUILD_SHARED_LIBS)` 添加 `test_opus_*`。
  - 方案 §3：排除 `tests/`；§10 步骤 1：接入后 `make test-core` 仍须通过。
  - `Makefile:48-51`：`make test-core` 不传额外 `-DOPUS_*`。
- **修改建议：** 在 `add_subdirectory(third_party/opus)` **之前**把 opus 选项写成 CACHE，并且**临时关闭、事后恢复**父级 `BUILD_TESTING`，例如：

  ```cmake
  set(_jk_build_testing ${BUILD_TESTING})
  set(BUILD_TESTING OFF)
  set(OPUS_BUILD_TESTING OFF CACHE BOOL "" FORCE)
  set(OPUS_BUILD_PROGRAMS OFF CACHE BOOL "" FORCE)
  set(OPUS_INSTALL_PKG_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
  set(OPUS_INSTALL_CMAKE_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
  add_subdirectory(third_party/opus EXCLUDE_FROM_ALL)
  set(BUILD_TESTING ${_jk_build_testing})
  ```

  不要全局 `set(BUILD_TESTING OFF)`，否则 core 自己的 CTest 也会被关掉。方案第 1 步必须用这条路径验证，不能用「独立配 opus」代替。

### B-3 静态库 `jkmeeting_core` 未规定 libopus 如何传到最终链接

- **位置：** 方案 §3.2、§4、§9；`core/CMakeLists.txt` 第 11–28 行；`apple/CMakeLists.txt` 第 15–28 行；`apple/project.yml` Shared / App / Tests
- **问题描述：** 方案强调 pimpl、公开头不包含 `opus.h`，容易把 opus 链成 `PRIVATE`。`jkmeeting_core` 是 `add_library` 静态库；现有测试辅助函数只 `target_link_libraries(${name} PRIVATE jkmeeting_core)`。CMake 对静态库的 PRIVATE 依赖**不会**进入使用方的链接接口。`opus_encoder.cpp` 一旦进入 archive，`opus_encoder_test`、`ogg_opus_encoder_test`、加上编码后的 `audio_session_bench`、以及使用 `JKMeetingOggOpusEncoder` 的桥接测试，都会在链接期出现 `_opus_encode` / `_opus_encoder_create` 未定义。Apple 侧已知事实只证明「三平台 xcframework 可生成」，没有证明 App / XCTest 已链上；`project.yml` 里最终产物是 App 与两个 test bundle，它们目前只依赖 Shared 静态库。
- **依据：**
  - `core/CMakeLists.txt:11-28`：`add_library(jkmeeting_core ...)`（静态）+ `jkmeeting_core_test` 只链 `jkmeeting_core`。
  - `apple/CMakeLists.txt:21,28`：桥接库和测试只链 `jkmeeting_core` / `jkmeeting_ios_bridge`。
  - CMake `target_link_libraries`：静态库的 PRIVATE 依赖不传播。
  - `apple/project.yml:17-44,83-108`：`JKMeetingShared-*` 为 `framework.static`；`JKMeeting-*` 与 `*Tests` 只 `dependencies: target: JKMeetingShared-*`。
- **修改建议：**
  1. 方案写死：`target_link_libraries(jkmeeting_core PUBLIC opus)`（或 `PRIVATE opus` + `INTERFACE opus`）。公开 C++ 头仍不得 `#include <opus.h>`；opus 的 PUBLIC include 泄漏到测试目标可以接受。
  2. 更新 `jkmeeting_core_test()`，需要直接调 `opus_decode` 的测试再显式 `target_link_libraries(... opus)`。
  3. `project.yml` 的 **App 与两个 Tests target** 同样声明 `framework: third_party/Opus.xcframework`（`embed: false`），不要只挂在 Shared 上。实施时用一次失败的链接日志或 `nm` 证明符号已解析；「xcframework 生成成功」不能代替这一步。

---

## 重要问题

### I-1 RFC 3533：长度为 255 倍数的包必须以 lacing `0` 收尾

- **位置：** 方案 §4.2、§8（只举例 600 字节包）
- **问题描述：** 255 表示「本段 255 字节且包未结束」。包长为 255、510、765、1020、1275 时，必须再写一个 `0`，否则解析器会认为包仍在继续。24 kbps VBR 的 20 ms 包多数偏小，但 B1 要求符合 RFC 3533；1275 又是方案自己采用的单包上限。
- **依据：** RFC 3533 §6：「A packet of 255 bytes (or a multiple of 255 bytes) is terminated by a lacing value of 0。」libogg `ogg_stream_iovecin` 使用 `lacing_vals = bytes/255+1`，最后一项为 `bytes%255`（含 0）。
- **修改建议：** 实现按 `floor(n/255)` 个 255 + 一个 `n%255`（可为 0）。测试除 600 外增加 255、510、1275。封页条件按「加入该包后的 lacing 数」，1275 字节包占 6 个 lacing（255×5+0）。

### I-2 `sample_count` / `EncodedPacket.frames` 与 ADR 0002、granule 每声道语义未对齐

- **位置：** 方案 §4.1；`core/include/jkmeeting/audio_processor.h:9-15`；`core/src/audio_session.cpp:26,70`；RFC 7845 §4
- **问题描述：** 现有 core 里 `sample_count` 是交错后的总采样值，`frames` 是每声道帧数。`opus_encode` 的 `frame_size` 也是每声道。新 API 写 `encode(const int16_t* samples, std::size_t sample_count)` 和 `EncodedPacket.frames`「以输入采样率计」，没有说是否除以 `channels`。算错会把立体声 granule 加倍。RFC 7845 §4：granule 按 48 kHz、**每声道**计数，「stereo 不加倍」。
- **依据：** ADR 0002：「`sample_count` 表示总采样值数量而不是每声道帧数」。`audio_session.cpp:26`：`frame_count = sample_count / channel_count`。RFC 7845 §4。
- **修改建议：** 公开头写清：`sample_count` 与 ADR 0002 相同（总采样值，必须能被 `channels` 整除）；`EncodedPacket.frames` 与 `input_frames()` 为每声道帧数；granule 用 `pre_skip + frames * 48000 / input_rate`（Opus 合法采样率都能整除，不要对整段再 `round` 一次以免和逐包累加不一致）。立体声单测至少断言 granule 不按交错样本加倍。

### I-3 B3「会话 + 编码」总耗时占比没有对应测量

- **位置：** 需求 B3；方案 §8、§10 步骤 3；`core/bench/audio_session_bench.cpp`；`Makefile:53-57`
- **问题描述：** B3 有两句：30 s / 16 kHz 单声道编码 RTF P95 < 1；**会话加编码**总耗时占比 < 20%。方案只给编码 RTF（编码耗时 / 音频时长）。现有 bench 只跑 `AudioSession`，结果写入 `output/p2-1-core-bench.jsonl`。`RecordingPipeline.coreProcessingNanoseconds` 只包住 `session.process/finish`，不含 `writer.append` 里的编码。
- **依据：** 需求文档 P2-2 验收 B3。`audio_session_bench.cpp:36-51,91-101`。`RecordingPipeline.swift:42-50,94-102`。`Makefile:53-57`。
- **修改建议：** bench 增加一条 `audio_session_then_ogg_opus`（48 kHz 会话输出再 16 kHz 编码，或直接 16 kHz PCM 编码 + 单独一行 session+encode），输出到 `output/p2-2-opus-bench.jsonl`，不要追加到 p2-1 文件。P95 同时报 RTF 和占比。

### I-4 服务端白名单会打断 `p1-experiments.py`，方案对调用方的判断不成立

- **位置：** 方案 §6；`server/scripts/p1-experiments.py`；`server/internal/httpapi/recording_handler.go:207-213`
- **问题描述：** 方案写「已知调用方（App、loadbench、冒烟和基线脚本）都使用 `audio/mp4`，不受影响」。`grep` 表明 App、`loadbench` 默认、`smoke.sh`、`smoke-asr.sh`、`measure-p0-baselines.py` 确实用 `audio/mp4`。但 `p1-experiments.py` 用 `audio/mp4-p1-bench` 做 HTTP 创建/压测，并用 `audio/mp4-p1-%` 做清理。白名单按精确匹配后，这些 POST 会 400，P1 复现实验会断。直接 SQL 插入的 `audio/mp4-p1-1m` / `audio/mp4-p1-backlog` 不受影响。
- **依据：**
  - `p1-experiments.py:147,194,246,372`：`content_type` 为 `audio/mp4-p1-bench`。
  - `p1-experiments.py:116,216,266,519`：`DELETE ... content_type LIKE 'audio/mp4-p1%'`。
  - `recording_handler.go:207-213`：当前只拒绝空 `content_type`，对象键写死 `audio.m4a`。
- **修改建议：** 文件清单加上 `server/scripts/p1-experiments.py`。压测改回 `audio/mp4`，清理标记改用 `user_id` 或独立前缀，不再把 `content_type` 当实验标签。白名单只接受精确的 `audio/mp4` 与 `audio/ogg`（不要带 `codecs=`）。

### I-5 CMake 路径必须显式打开 NEON，不能只写在 xcframework 脚本里

- **位置：** 方案 §3.1；`Makefile:48-57,59-62`
- **问题描述：** macOS 上 `CMAKE_SYSTEM_PROCESSOR=arm64`，对不上 opus 判断用的 `aarch64`，`OPUS_PRESUME_NEON` 默认为 OFF。`make test-core` / `bench-core` / `test-apple-bridge` 都不传该选项。只在 xcframework 脚本里打开 NEON，CMake 路径的 RTF 会偏慢，B3 证据也不完整。
- **依据：** 方案 §3.1 已实测该行为。opus `OpusConfig.cmake`：仅 `CMAKE_SYSTEM_PROCESSOR MATCHES "aarch64"` 或 iOS 时才自动 `OPUS_PRESUME_NEON ON`。`Makefile` 的 cmake 调用无 `-DOPUS_PRESUME_NEON`。
- **修改建议：** 在 core 的 CMake 里，Apple Silicon / `arm64` 上于 `add_subdirectory` 之前 `set(OPUS_PRESUME_NEON ON CACHE BOOL "" FORCE)`。桥接测试走同一份 core CMake，不要依赖 xcframework。

### I-6 Swift `FileHandle` 必须用会抛错的写入 API

- **位置：** 方案 §5、§7；现有代码无 `FileHandle` 用例
- **问题描述：** 方案写「沿用 `FileHandle` 抛出的错误」。旧的 `write(Data)` 不抛错，失败时可能只留下不完整文件。现有 `AppleAudioFileWriter` 走 `AVAudioFile.write`，没有现成范本。
- **依据：** 方案 §7 错误表。Apple 文档：`write(contentsOf:)` 会抛错；`write(_ data: Data)` 已弃用且不抛 `Error`。
- **修改建议：** 方案写死：`FileHandle(forWritingTo:)` + `write(contentsOf:)` + `synchronize`/`close`，全部用 `try`。空 `NSData` 不写盘。`alreadyFinished` / 二次 `finish` 与现有 writer 对齐。未知 `pathExtension` 在 `RecordingAPIClient` 直接失败，不要默认为 `m4a`。

### I-7 裁剪 third_party 源码时必须留下许可证文件

- **位置：** 方案 §3
- **问题描述：** 排除清单写了 `dnn/`、`tests/`、`doc/`、meson，没写保留 `COPYING` / `AUTHORS` / 版权声明。BSD-3-Clause 要求保留版权与条件文本。
- **依据：** 方案 §3（BSD-3-Clause）。Xiph opus 发行包根目录的 `COPYING`。
- **修改建议：** `core/third_party/README.md` 把「必须保留的许可证文件」写成清单；裁剪脚本不得删除它们。

### I-8 客户端上传测试仍按死 `audio.m4a`，ogg 路径缺少对等用例

- **位置：** 方案 §5、§8；`apple/Shared/Recording/RecordingAPIClient.swift:105,133,157`；`apple/SharedTests/RecordingAPIClientTests.swift:35-64`
- **问题描述：** 方案要按扩展名映射 Content-Type 和 `/audio.<ext>`。现有单测的预签名路径、PUT `Content-Type`、本地文件都是 `.m4a`，且创建 JSON **没有**断言 `content_type`。只改实现、不补 ogg 用例，回归测不到新主链路。
- **依据：** `RecordingAPIClient.swift:105` 写死 `.../audio.m4a`；`:133,157` 写死 `audio/mp4`。`RecordingAPIClientTests.swift:35,45,64`。
- **修改建议：** 保留 m4a 用例（旧文件兼容）。新增 `.ogg` 用例：创建 body 为 `audio/ogg`，PUT 为 `audio/ogg`，路径为 `.../audio.ogg`。再加一条未知扩展名负向用例。

---

## 建议

1. **Xcode 静态库是否自动传播 xcframework（待确认）。** 方案只给 Shared 加 `Opus.xcframework`。Xcode 27 有可能把依赖传到 App/Tests，但不是可依赖的约定。与 B-3 一起，在 App 与 Tests 上重复声明，或用一次 `xcodebuild` 链接日志确认。不要把「xcframework 已生成」写成 Apple 测试已通过。
2. **`smoke-asr` 默认必须仍是 m4a。** `AUDIO_FORMAT=ogg` 作为显式开关。`Makefile:105-107` 与现有 `smoke-asr.sh` 无条件走 AAC。改默认会把 P0 冒烟和 B7 绑死在 opus 样本上。
3. **VOIP + 1 kHz 正弦的 SNR ≥ 15 dB（待确认）。** `OPUS_APPLICATION_VOIP` + `OPUS_SIGNAL_VOICE` 针对语音。15 dB 门限较松，仍可能在 SILK 上抖。测试应锁定生产配置，不要为过 SNR 改成 `OPUS_APPLICATION_AUDIO`；失败则换语音样或把门限和信号写进报告。
4. **AVAudioFile 是否执行 granule 裁尾（待确认）。** B5 允许 20 ms。未裁 pre-skip（约 6.5–10 ms）或末帧补零（最多 20 ms）仍可能过线。`durationMs` 继续用编码器输入样本数。XCTest 对 `AVAudioFile.length` 与 `AVAudioPlayer.duration` 分开断言，超差就记进报告，不要改 granule 去迁就解码器。
5. **VBR 体积。** 24 kbps 是目标，不是上限。5 分钟最坏大约 1.3–1.5 MB 量级。服务端 `size_bytes` 只是元数据；报告里记一笔典型/峰值体积即可。
6. **Whisper。** `start-whisper.sh:17` 已有 `--convert`；Worker 临时文件为 `*.audio`（`s3.go:81`）。与方案一致。B4/B7 按方案做即可。默认冒烟仍用 m4a，避免环境无 `libopus` 时 P0 冒烟全挂。
7. **线程安全。** `JKMeetingAudioSession` 已标明非线程安全。编码器同样写明，并规定只在 `PipelineStore` 串行队列上使用。libopus 编码器实例不能并发 `encode`。
8. **OpusTags 二进制布局。** 方案只说 vendor 用 `opus_get_version_string()`、不写用户注释。测试应检查：`"OpusTags"` + uint32 LE vendor 长度 + 字符串 + uint32 LE `0`。多字节字段一律 LE（RFC 7845 §5.1、§5.2）。CRC 按小端写入页头偏移 22（Ogg framing）。
9. **父工程启用 C 语言。** `project(jkmeeting_core LANGUAGES CXX)` 与 `project(jkmeeting_ios_bridge LANGUAGES CXX OBJCXX)` 都没有 C。嵌套的 `project(Opus LANGUAGES C)` 通常能补上，仍建议在 core 写 `enable_language(C)`，避免工具链探测失败。
10. **`serial == 0`。** `UInt32.random` 可能为 0。Ogg 允许，部分工具会抱怨。排除 0。
11. **xcframework 版本戳。** 除 opus 版本外，纳入 `OPUS_PRESUME_NEON`、`CMAKE_SYSTEM_PROCESSOR`、SDK 名称，避免改旗标不重建。
12. **`headers()` 调用次数。** 只允许一次；重复调用应失败或返回空，防止壳层写两份头。
13. **现有 Apple 管理器测试会变成集成测试。** `AppleRecordingManagerTests` 在切换写入器后会真实编码（`SimulatedCapture` 为 8000 样本 / 0.5 s，25 包，**不会**碰到 50 包 EOS 边界）。它们依赖 xcframework 链接和 `AVAudioFile` 读 ogg。方案应点名这些测试成为 B5 的补充证据，而不是继续假定它们只测 AAC。

---

## 已确认无问题的项

| 项 | 说明 |
| --- | --- |
| B1～B7 主映射 | 除 B3 后半句（I-3）外，每条都有设计与验证入口：core 页解析、编解码回放、bench、实验报告、双端 XCTest、handler 单测、`AUDIO_FORMAT=ogg make smoke-asr`。 |
| 未越出 P2-2 | 不在 core 提供解码/解 Ogg 公开 API；不改 schema；不迁旧 m4a；不做 Intel/x86_64；不启用 DRED/OSCE/深度 PLC；不改 P2-3 回放实现、不做 P2-4 转码。CLI / bench / ADR 0006 / 实验报告服务于验收，不是范围膨胀。 |
| 容器选型 | Ogg Opus 同时覆盖 Apple 读、FFmpeg/Whisper、未来 Android；CAF 不能用 FFmpeg 封装。与需求调研结论一致。 |
| OpusHead | 19 字节、version=1、channels、pre-skip、原始采样率、增益 0、映射族 0，符合 RFC 7845 §5.1。 |
| pre-skip 单位 | RFC 7845 §5.1 明确是 **48 kHz 样本**。`OPUS_GET_LOOKAHEAD`（编码器采样率）换算到 48 kHz 正确。 |
| 头页 granule | 两页头、各页 granule 0，符合 RFC 7845 §4。 |
| CRC | 多项式 `0x04C11DB7`、初值 0、不反射、不终异或、计算时 CRC 字段置 0。独立复算 `"123456789"` → `0x89A1897F`，与 libogg / 方案一致。 |
| 包上限与帧长 | 1275 字节（RFC 6716）、帧长 10/20/40/60 ms、VOIP + VBR，合理。 |
| flush 补零与时长 | 用 `final_granule = pre_skip + input_frames_48k` 裁尾，方向符合 RFC 7845 §4.4（前提是 B-1 把末包留在 EOS 页）。 |
| core 模块边界 | 编码器/muxer 只产内存字节；文件 IO 在壳层与 `core/tools/`。不引入 AVFoundation / 文件 / socket。符合 AGENTS.md §3 与 ADR 0005「编码器接在会话之后」。 |
| pimpl | 公开头不暴露 `opus.h`；Xcode 编 `core/src/*.cpp` 时用 `HEADER_SEARCH_PATHS` 指向 `core/third_party/opus/include`，方向正确。 |
| 所有权方向 | 输入为调用期借用指针，输出为 `vector` 拥有缓冲；内部帧缓存需自备拷贝，不保存调用方指针。与 ADR 0002 一致（语义书写见 I-2）。 |
| 旧 m4a 兼容 | 白名单保留 `audio/mp4`；`AppleAudioFileWriter` 保留给现有测试；Worker/`DownloadToTemp` 用内容识别、扩展名 `.audio`；回放按库里的 `object_key`。不迁历史对象。 |
| 主调用方 `audio/mp4` | App、`loadbench` 默认、`smoke.sh`、`smoke-asr.sh`、`measure-p0-baselines.py` 均为 `audio/mp4`。收紧白名单不伤这些路径（P1 实验脚本除外，见 I-4）。 |
| gitignore | `.gitignore:5` 已有 `apple/third_party/`，xcframework 不入库已覆盖。 |
| Xcode 源码收录 | `project.yml` 的 `../core/src` 会自动带上新的 `.cpp`，不会编到 `third_party/opus`。 |
| 桥接测试不依赖 xcframework | `apple/CMakeLists.txt` 的 `add_subdirectory(core)` 在 B-2/B-3 修好后可以只走 CMake opus。 |
| Android 复用 | P4 对同一份 CMake `add_subdirectory` 可行；NDK 通常给出 `aarch64`，NEON 比 macOS 主机路径更顺。本次不实现，符合非目标。 |
| 测试不是空断言 | CRC 已知值、页字段、SNR、分块字节一致、对象键后缀、400、AVAudioFile 时长门限，都是可证伪断言。 |
| 方案已写风险 | 模拟器≠真机、24 kbps 转写差异、xcframework 首次耗时、旧文件不迁移——这几条够用。 |

---

## 需求覆盖速查

| 验收 | 方案对应 | 评审 |
| --- | --- | --- |
| B1 页结构 / CRC / BOS / EOS / OpusHead / OpusTags / granule | §4.2、§8 muxer 测试 | 设计方向对；B-1、I-1 未补前不能声称符合 RFC |
| B2 解码长度、SNR、分块不变 | §8 encoder / 组合测试 | 通过（SNR 信号见建议 3） |
| B3 RTF 与会话+编码占比 | §8 bench | RTF 有；占比缺（I-3） |
| B4 ffprobe / whisper 对比 | §8 互通 + 实验报告 | 通过 |
| B5 AVAudioFile / AVAudioPlayer ≤ 20 ms | §8 SharedTests | 通过（依赖 B-1、B-3） |
| B6 `audio/ogg` → `audio.ogg`，未知 400 | §6、§8 | 通过；须处理 I-4 |
| B7 `AUDIO_FORMAT=ogg make smoke-asr` | §6、§10 | 通过；默认保持 m4a |

---

## 评审范围外（不阻断，也不当作已验证）

- 未重新下载 opus-1.6.1 核对 SHA256；沿用方案与官方 `SHA256SUMS.txt` 已核对的陈述。
- 未在本机重跑 xcframework / Simulator；沿用方案作者的已知实测，且未发现与仓库现状矛盾。
- 真机 Ogg 解码留在 P2-3，与方案一致。
