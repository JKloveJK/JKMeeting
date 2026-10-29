# P4 Android 接入技术方案

- 日期：2026-10-29
- 状态：实施中
- 需求：[P4 Android 端接入需求](../requirements/2026-10-29-p4-android-requirements.md)

## 1. 关键取舍

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| 构建 | Gradle Kotlin DSL + AGP 8.7 / Kotlin 2.0 / Compose BOM；Gradle Wrapper 8.11 | 本机系统 Gradle 9.6 过新，Wrapper 锁定可复现版本 |
| 原生构建 | `externalNativeBuild.cmake` 直接 `add_subdirectory` 仓库 `core/` | 同一份 CMake 工程即是“core 零修改”的最强证据；libopus 纯 C 可交叉编译 |
| ABI | 仅 `arm64-v8a`（本机模拟器镜像架构） | 缩小构建面；x86_64 待真机/CI 需要时再加 |
| 桥接面 | JNI 只暴露 `createSession / processFrame / finish / stats` | 与 Apple ObjC++ 桥接面等价，便于双端行为对比 |
| 文件格式 | 16 kHz S16LE WAV（Kotlin 侧写头） | 无第三方依赖；回放走 `MediaPlayer` |
| 采集 | `AudioRecord` 48 kHz 单声道，1024 样本/帧，`sample_time` 用累计样本数 | 与 Apple 壳层喂帧口径一致，core 时间轴可直接复用 |

## 2. 文件

- `android/settings.gradle.kts`、`android/build.gradle.kts`、`android/gradle/wrapper/*`、`android/app/build.gradle.kts`
- `android/app/src/main/AndroidManifest.xml`
- `android/app/src/main/java/com/jkmeeting/android/MainActivity.kt`：Compose UI + ViewModel。
- `android/app/src/main/java/com/jkmeeting/android/AudioRecorder.kt`：`AudioRecord` 采集与分帧。
- `android/app/src/main/java/com/jkmeeting/android/WavFile.kt`：纯 Kotlin WAV 读写（可 JVM 单测）。
- `android/app/src/main/java/com/jkmeeting/android/NativeCore.kt` + `android/app/src/main/cpp/jni_bridge.cpp` + `CMakeLists.txt`：JNI 桥。
- `android/app/src/test/`：JVM 单测；`android/app/src/androidTest/`：JNI instrumented 测试。

## 3. 验证

- `make test-android`：JVM 单测 + `assembleDebug`。
- 模拟器：`connectedDebugAndroidTest` 跑 JNI 一致性；`am start -W` 测冷启动；手动录音→回放留运行记录（模拟器麦克风走宿主机）。
