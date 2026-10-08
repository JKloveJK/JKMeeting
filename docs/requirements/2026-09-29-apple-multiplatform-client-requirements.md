# Apple 双端客户端需求

- 日期：2026-09-29
- 状态：已确认
- 关联：`docs/客户端全栈学习路线.md`、`docs/plans/2026-09-29-p0-minimal-loop-design.md`、`docs/decisions/0002-core-audio-frame-ownership.md`

## 背景与问题

JKMeeting 当前已具备可在 macOS 上独立验证的 C++ 音频 core 与 ObjC++ bridge，但尚未形成可运行的 App。项目学习目标包含跨平台客户端抽象：如果 P0 只建立 iOS 壳层，Mac 平台的复用边界会延后验证，且本机开发和真实产品形态之间缺少直接反馈。

用户确认 iOS 与 macOS 都应作为 P0 的一等客户端。二者需要复用同一份 C++ core、ObjC++ bridge 和尽可能多的 SwiftUI 业务层；平台差异只能留在 Apple 壳层。

## 目标

1. 建立一个可由 Xcode 打开的 Apple 双 target SwiftUI 工程，分别提供 iOS 与 macOS App target。
2. 在不接入真实麦克风前，以确定性 S16LE PCM 从 Swift 调用现有 ObjC++ bridge 与 C++ core，并在两个平台上显示处理结果。
3. 建立可复现的构建与测试证据：macOS 单元测试、macOS App 构建、iOS Simulator App 构建。
4. 明确 Xcode 与 CMake 的职责边界，并让后续真实录音、文件回放、上传和转写可以在独立切片中接入。

## 范围

- 将 Apple 平台目录从 `ios/` 重命名为 `apple/`。
- 使用 `apple/project.yml` 作为 XcodeGen 工程声明来源，生成并提交 `apple/JKMeeting.xcodeproj`。
- 建立独立的 `JKMeeting-iOS` 与 `JKMeeting-macOS` App target。
- 建立各自独立的 iOS / macOS XCTest target 和 Scheme 测试动作。
- 从 Swift 通过受控 facade 调用 `JKMeetingCore`，将 bridge 错误映射为 Swift 的类型化错误。
- 新增共享的诊断 view model 与 SwiftUI 页面，用确定性 PCM 展示 core 自检结果。
- 将真实构建、单测和 Simulator 构建命令接入 `Makefile`。
- 记录 Apple 双端架构 ADR 与构建验证证据。

## 非目标

- 不接入 `AVAudioEngine`、麦克风权限、音频会话、录音中断处理或真实采集。
- 不写本地音频文件，不做回放，不接入网络、上传、服务端、ASR 或 Agent。
- 不引入 Android、JNI、Opus、FFmpeg、云资源或新的第三方运行时依赖。
- 不以 Simulator 结果替代 iPhone 或 Mac 的真实麦克风验证。

## 约束与依赖

- `core/` 必须保持纯 C++；禁止包含 Apple SDK、文件 IO、网络、UI 或平台状态。
- `apple/Bridge/JKMeetingCore` 是 Swift 与 C++ 的唯一桥接边界；Swift 不得直接包含 C++ 头文件。
- Xcode target 直接编译 `JKMeetingCore.mm` 与 `core/src/audio_processor.cpp`，并显式配置 C++20、ARC、必要头文件搜索路径和 Foundation。
- CMake / CTest 仅用于独立验证 core 与 bridge；Xcode target 不链接 CMake 生成的静态库，避免源文件重复编译或架构冲突。
- iOS / macOS 测试 target 必须独立定义，不能共享同一个 XCTest bundle。
- 项目已安装 Xcode 27 与 XcodeGen；生成的 `.xcodeproj` 需提交，`project.yml` 是唯一工程声明来源。
- 所有新增文档使用简体中文；命令、路径、标识符与必要术语保留原始拼写。

## 验收标准

| 编号 | 判据 | 证据 |
| --- | --- | --- |
| A1 | `ios/` 已重命名为 `apple/`，原 bridge 测试行为不变 | `make test-apple-bridge` 通过 |
| A2 | Swift facade 能将四个确定性 PCM 样本从 4 Hz 重采样到 2 Hz，并保留 metadata | macOS XCTest 断言样本、采样率、声道数、时间戳 |
| A3 | 三字节非法 PCM 会返回 `CoreProcessorError.invalidPCM` | macOS XCTest 通过 |
| A4 | 诊断 view model 能显示 core 自检输出的采样率、声道数和样本数 | macOS XCTest 通过 |
| A5 | macOS App target 可构建且 macOS XCTest 通过 | `make build-apple-macos`、`make test-apple-macos` 通过 |
| A6 | iOS App target 可针对指定 Simulator 构建 | `make build-apple-ios-simulator` 通过 |
| A7 | `make verify` 汇总 core、bridge、macOS 单测及两个 Apple target 构建 | 完整命令输出通过 |
| A8 | Apple 双端架构、构建证据与真机限制有文档记录 | ADR、`docs/metrics.md`、P0 设计均已更新 |

## 依赖与待确认问题

- 依赖：本机 Xcode 27、XcodeGen、可用的 iOS 26.2 Simulator。
- 待确认问题：无。真实麦克风采集与物理设备验证将在后续独立需求中处理。
