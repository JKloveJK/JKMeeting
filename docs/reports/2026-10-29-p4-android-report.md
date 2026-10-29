# P4 Android 接入报告

- 日期：2026-10-29
- 关联：[P4 需求](../requirements/2026-10-29-p4-android-requirements.md)、[P4 方案](../plans/2026-10-29-p4-android-implementation.md)
- 环境：Android Studio SDK（platform 34、NDK 27、CMake 3.22.1）、模拟器 `test_avd`（android-34 google_apis arm64-v8a，4 核）、Gradle Wrapper 8.11.1 + AGP 8.7.3 + Kotlin 2.0.21。

## 1. 验收对照

| 编号 | 结论 | 证据 |
| --- | --- | --- |
| A1 core 改动量 | **通过（零修改）**：为 Android 接入未改任何既有 core 源码/头文件；`git diff HEAD -- core/` 仅 `core/CMakeLists.txt` +2 行，且属于 P5 抖动缓冲的新增文件登记，与 Android 无关 | `git diff` 统计；JNI 桥在 `android/app/src/main/cpp/` |
| A2 主链路 | **部分通过**：模拟器安装启动、Compose UI 渲染正常；录音→回放的真人操作未在无头模拟器完成（`-no-audio` 启动，无麦克风输入），核心处理段由 A3 的 instrumented 测试等价覆盖 | `adb install` 成功、`am start` 启动；缺口见 §3 |
| A3 JNI 一致性 | **通过**：30 秒 48 kHz 正弦经 `AudioSession` 后 16 kHz 输出样本与期望相差 ≤ 1 帧，core 统计输出帧一致 | `connectedDebugAndroidTest`：1 个测试通过（`NativeCoreInstrumentedTest`） |
| A4 冷启动 | **口径分化**：`am start -W` TotalTime 937～1134 ms（模拟器，含系统侧开销）未达 800 ms；App 内口径（进程创建→首帧）276～535 ms 达标。真机复测前标为待确认 | `am start -W` ×3 + logcat `cold_start_ms` |
| A5 构建与单测 | **通过**：JVM 单测（WAV 往返）+ `assembleDebug` 成功，APK 8.7 MB（含 arm64 原生库） | `./gradlew testDebugUnitTest assembleDebug` BUILD SUCCESSFUL |

## 2. 实施中遇到的真实差异（Android vs Apple）

1. **vendored opus 的裁剪文件**：Android 上 opus 的 RTCD 分支会引用已裁剪的 `dnn/arm/arm_dnn_map.c`，桌面 Apple 因检测路径不同未受影响。壳层以 `OPUS_DISABLE_INTRINSICS=ON` 走便携 C 实现解决（功能等价；Android 性能基线不在本阶段口径内），core 零修改。
2. **AGP 构建全部 CMake 目标**：core 桌面单测/bench 被顺带编译，NDK clang 对 designated initializer 省略字段的 `-Werror` 口径比桌面严。壳层为这些目标单独降级 `missing-field-initializers` 警告，未改 core 警告配置。
3. **NDK 由 AGP 自动装了 27.0**（本机原有 25.1），构建可复现性靠 Gradle Wrapper + AGP 版本锁定。

## 3. 已知缺口

- 模拟器无头模式下未做真人录音→回放操作验证（需带音频的模拟器或真机）。
- 冷启动 `am start -W` 口径在模拟器上未达标，需真机复测才能关闭 A4。
- Android 端未接上传/转写网络链路（需求明确后置，单独立项）。

## 4. 抽象质量结论

第二个平台接入未要求 core 做任何让步：`AudioSession` 的帧/时间戳抽象（`sample_time` 用输入采样率样本位置）在 JNI 侧直接成立。若后续 Android 需要 Opus 编码，其 CMake 路径已验证可链接同一 `jkmeeting_core` 静态库。
