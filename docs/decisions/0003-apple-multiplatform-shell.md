# 0003：P0 采用 iOS 与 macOS 双端 Apple 壳层

- 日期：2026-09-29
- 状态：已接受
- 关联：`docs/requirements/2026-09-29-apple-multiplatform-client-requirements.md`、`docs/plans/2026-09-29-p0-minimal-loop-design.md`

## 背景

P0 已完成 C++ core 与 macOS Foundation ObjC++ bridge 的独立验证，但尚未形成可运行的 App。若只在 iOS 建立产品壳层，Mac 平台对共享 SwiftUI、ObjC++ bridge 与 C++ core 的复用验证会被推迟，无法尽早发现 Apple 平台边界问题。

用户已确认 iOS 和 macOS 都是 P0 的一等客户端。当前切片仅建立双端工程、确定性 PCM 自检与构建证据；真实采集、回放、网络和服务端仍在后续独立切片完成。

## 决定

1. 将 Apple 平台目录统一命名为 `apple/`，避免以 `ios/` 承载 macOS 代码。
2. 使用 `apple/project.yml` 作为唯一工程声明来源，由 XcodeGen 生成并提交 `apple/JKMeeting.xcodeproj`。
3. 建立独立的 `JKMeeting-iOS`、`JKMeeting-macOS` App target、`JKMeetingShared-iOS`、`JKMeetingShared-macOS` framework target，以及各自独立的 XCTest target 和 Scheme 测试动作。
4. 两个 Shared framework target 均为静态 framework，直接编译 `apple/Bridge/JKMeetingCore.mm` 与 `core/src/audio_processor.cpp`；显式设置 C++20、ARC、Foundation 与头文件搜索路径。App target 仅链接各自平台的 Shared framework，不直接编译 bridge 或 C++，也不嵌入动态 framework。
5. iOS / macOS XCTest target 是 hostless 单元测试 bundle，各自依赖并通过 `@testable import` 导入对应 Shared framework，不以 SwiftUI App 作为测试宿主。这样测试不会启动 App 生命周期，也不会耦合 UI 进程退出行为。
6. CMake / CTest 继续仅用于独立验证 C++ core 与 ObjC++ bridge；Xcode target 不链接 CMake 生成的静态库，避免重复符号、架构冲突和构建链路耦合。
7. Swift 只通过 Shared framework 中的公开 Swift facade 访问 `JKMeetingCore`；bridge 公开稳定的错误 domain 与错误码，facade 将其映射为类型化错误。
8. 本切片不接入 `AVAudioEngine`、麦克风权限、文件写入、回放、网络、上传、ASR 或 Agent。

## 依据

- `core/` 的借用输入与拥有型输出已由 ADR 0002 固化；两端直接复用同一实现可以验证其生命周期与编译边界。
- iOS 与 macOS 都能使用 SwiftUI 与 Objective-C++，适合作为 Android 接入前的低成本跨平台验证。
- 保持 CMake 和 Xcode 两套构建链路职责单一，能分别验证纯 C++ / bridge 与真实 Apple target，不依赖交叉链接产物。
- 初版 hosted XCTest 启动 SwiftUI App 后无法完成测试，而 hostless XCTest 无法直接链接 App 模块；将可测实现放入平台 Shared framework 后，App 与测试都通过同一模块依赖，可以消除该测试宿主耦合。

## 后果

- P0 增加了 macOS App 的构建与测试成本，并新增每个平台一个 Shared framework；代价是多两个 target，但可更早验证 Apple 双端复用质量和可测试边界。
- iOS Simulator 与 macOS 构建、确定性 PCM 测试只能证明工程和 bridge 集成；不能证明真实麦克风采集。iPhone 与 Mac 的真机 / 本机录音 smoke 测试将在后续采集切片单独执行。
- `project.yml` 与生成的 `.xcodeproj` 必须保持一致；验证时执行 `xcodegen generate` 后用 Git diff 检查工程是否漂移。
