# 苹果双端客户端实施计划

> **执行要求：**仅在 `docs/reviews/` 中的独立评审结论为“通过”后执行代码任务。每项新增行为先写失败测试，再进行最小实现；除无行为变更的目录迁移外，不得跳过红测。

**目标：**建立可作为 iOS 与 macOS 一等客户端运行的 SwiftUI App 工程，复用同一份 ObjC++ / C++ 音频 core，并在接入真实音频采集前形成可复现的工程构建、Swift bridge 和诊断自检证据。

**架构：**`apple/project.yml` 是唯一工程声明来源。XcodeGen 生成并提交 `apple/JKMeeting.xcodeproj`；每个平台均由一个 App target、一个 Shared framework target 和一个 hostless XCTest target 组成。Shared framework 直接编译 `JKMeetingCore.mm` 与 `audio_processor.cpp` 并承载可测试 Swift facade；App 只依赖 / 嵌入对应 framework，XCTest 只依赖 / 导入对应 framework。CMake / CTest 继续只验证纯 C++ core 与 ObjC++ bridge；Xcode target 不链接 CMake 产物。

**技术栈：**Swift 6、SwiftUI、Objective-C++、C++20、Foundation、Xcode 27、XcodeGen、CMake/CTest、xcodebuild、iOS Simulator、macOS。

---

## 约束与验收

- `core/` 必须保持纯 C++；禁止引入 Apple SDK、文件 IO、网络、UI 或平台状态。
- `apple/Bridge/JKMeetingCore` 是 Swift 与 C++ 的唯一边界；Swift 不得包含 C++ 头文件。
- iOS 与 macOS 都是 P0 一等客户端；Android 仍属于 P4。
- 本计划不接入 `AVAudioEngine`、麦克风权限、文件写入、回放、网络、上传、ASR 或 Agent。
- Simulator 构建与确定性 PCM 测试不能代替 iPhone 或 Mac 的真实麦克风 smoke 测试。
- 每次执行 `xcodegen generate` 后必须检查 `.xcodeproj` 是否漂移；手工修改生成工程视为错误。

## 评审修订说明

| 评审问题 | 修订方式 |
| --- | --- |
| 工程尚不存在时不能运行 facade 红测 | 先建立双 target Xcode 工程和 XCTest target，再在已存在的测试环境中编写 facade 红测 |
| 两端不能共用 XCTest bundle | `JKMeeting-iOSTests` 与 `JKMeeting-macOSTests` 独立定义、独立 source membership 和 Scheme test action |
| Xcode / CMake 编译边界不清 | ADR 0003 已固定：Xcode 直接编译 `.mm + .cpp`；CMake 仅独立验证；禁止 Xcode 链接 CMake 库 |
| P0 双端范围在实施后才记录 | 已在编码前创建 ADR 0003 并更新 P0 设计 |
| Swift 没有稳定 bridge 错误契约 | 任务 4 先公开错误 domain / code，再映射至 `CoreProcessorError` |
| 迁移不满足红测要求 | 目录重命名标记为无行为变更例外，迁移前后分别运行 bridge 回归验证 |
| 诊断逻辑职责不清 | `CoreDiagnosticsViewModel` 独立于 SwiftUI View，拥有单独红测 |
| `.xcodeproj` 可能漂移 | 最终验证包含 `xcodegen generate` 和 `git diff --exit-code -- JKMeeting.xcodeproj` |

## 任务 1：确认 P0 双端范围与编译边界

**文件：**
- 新建：`docs/decisions/0003-apple-multiplatform-shell.md`
- 修改：`docs/plans/2026-09-29-p0-minimal-loop-design.md`
- 修改：`docs/requirements/2026-09-29-apple-multiplatform-client-requirements.md`

**步骤：**

1. 创建 ADR 0003，明确 iOS / macOS 是 P0 一等客户端；Xcode 直接编译 bridge / C++ 源，CMake 仅做独立测试。
2. 更新 P0 设计中的 Apple 客户端、音频入口和一句话范围；真实采集和物理设备 smoke 测试仍后置。
3. 检查需求文档、P0 设计与 ADR 的范围、非目标、编译边界一致。

**验证：**

```sh
git diff --check
```

预期：文档无空白错误；不存在将真实采集、网络或 CMake 静态库链接引入本切片的描述。

## 任务 2：迁移 Apple 平台壳层并保持现有行为

**文件：**
- 移动：`ios/` → `apple/`
- 修改：`.gitignore`
- 修改：`Makefile`
- 修改：`apple/README.md`
- 修改：`docs/plans/2026-09-29-p0-minimal-loop-design.md`

**步骤：**

1. 运行现有 `make test-ios-bridge`，记录迁移前 bridge / core 回归基线。
2. 运行：

```sh
git mv ios apple
```

3. 将 `.gitignore` 的 `ios/build/` 改为 `apple/build/`。
4. 将 `Makefile` 的 `test-ios-bridge` 重命名为 `test-apple-bridge`，并把全部 CMake 源 / 构建路径更新为 `apple/`。
5. 更新 `apple/README.md` 和 P0 设计中的目录引用；不改变 bridge / core 源码行为。
6. 运行迁移后的 bridge 回归验证。

**验证：**

```sh
make test-apple-bridge
git diff --check
```

预期：CTest 中 ObjC++ bridge 与 core 测试均通过；构建产物仍被 Git 忽略。

## 任务 3：先生成可测试的双 target Xcode 工程骨架

**文件：**
- 新建：`apple/project.yml`
- 新建：`apple/App/JKMeetingApp.swift`
- 新建：`apple/App/Resources/Info-iOS.plist`
- 新建：`apple/App/Resources/Info-macOS.plist`
- 新建：`apple/AppTests-iOS/PlaceholderTests.swift`
- 新建：`apple/AppTests-macOS/PlaceholderTests.swift`
- 新建：`apple/JKMeeting.xcodeproj/**`（由 XcodeGen 生成）

**步骤：**

1. 在 `project.yml` 定义 `JKMeeting-iOS`、`JKMeeting-macOS`、`JKMeeting-iOSTests`、`JKMeeting-macOSTests` 四个 target。
2. 为每个 App target 配置：
   - 共享 Swift 源码目录 `App/`；
   - `Bridge/JKMeetingCore.mm` 与 `../core/src/audio_processor.cpp`；
   - `../core/include` 头文件搜索路径；
   - C++20、Objective-C ARC 与 Foundation 链接；
   - 各自独立的 bundle identifier、Info.plist、Scheme 和 test action；
   - 各自稳定的 Swift 模块名：`JKMeeting_iOS` 与 `JKMeeting_macOS`；
   - `SWIFT_OBJC_BRIDGING_HEADER: Bridge/JKMeeting-Bridging-Header.h`。
3. 新建 `Bridge/JKMeeting-Bridging-Header.h`，其内容仅为 `#import "JKMeetingCore.h"`。`App/`、bridge 和 C++ 源仅属于 App target，禁止将 CMake 产物作为 target 依赖。
4. 为两个 XCTest target 建立明确且独立的 source membership、App 依赖和 Scheme test action：
   - `JKMeeting-iOSTests`：`type: bundle.unit-test`、`platform: iOS`、源码目录 `AppTests-iOS/`、依赖 `JKMeeting-iOS`，测试使用 `@testable import JKMeeting_iOS`；
   - `JKMeeting-macOSTests`：`type: bundle.unit-test`、`platform: macOS`、源码目录 `AppTests-macOS/`、依赖 `JKMeeting-macOS`，测试使用 `@testable import JKMeeting_macOS`；
   - 两个 Scheme 的 `test.targets` 只包含各自平台的 Tests target。
5. 初始仅添加能通过的占位测试，证明两个测试 target 与 Scheme 已可运行。
6. 执行 `xcodegen generate`，提交生成的 `.xcodeproj`。
7. 在 macOS 上运行 placeholder 测试，并分别构建 macOS 与 iOS Simulator App。

**验证：**

```sh
cd apple && xcodegen generate
xcodebuild test -project JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild build -project JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'platform=iOS Simulator,name=iPhone 17 Pro'
```

预期：测试和两个构建命令均成功；此时尚未存在 `CoreProcessor` 行为测试。

## 任务 4：先公开 bridge 错误契约，再以红测实现 Swift facade

**文件：**
- 修改：`apple/Bridge/JKMeetingCore.h`
- 修改：`apple/Bridge/JKMeetingCore.mm`
- 新建：`apple/App/Audio/CoreProcessor.swift`
- 新建：`apple/AppTests-macOS/CoreProcessorTests.swift`
- 新建：`apple/AppTests-iOS/CoreProcessorTests.swift`

**步骤：**

1. 将 `kJKMeetingCoreErrorDomain` 与 `JKMeetingCoreErrorCode` 从 `.mm` 私有定义迁移为 header 可见的稳定 Objective-C 错误契约；保持当前错误语义不变。
2. 在 macOS 测试 target 中先写 `CoreProcessorTests`：
   - 四个 S16LE 样本 `[0, 1000, 2000, 3000]` 从 4 Hz 重采样到 2 Hz，期望输出 `[0, 2000]`、采样率 2、单声道、时间戳 42；
   - 三字节 `Data` 期望抛出 `CoreProcessorError.invalidPCM`。
3. 运行 macOS 测试，确认因 `CoreProcessor` 尚不存在而失败；失败必须定位于缺失类型或 API，而不是工程、Scheme 或编译配置。
4. 以最小实现新增 `CoreProcessor`、不可变输出值类型和 `CoreProcessorError`，仅将 `JKMeetingCore` 的公开错误映射为 Swift 类型化错误。
5. 将同等行为测试加入 iOS 测试 target，确保两端测试源码和 bundle 独立。
6. 分别运行 macOS 与 iOS Simulator 测试。

**验证：**

```sh
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'platform=iOS Simulator,name=iPhone 17 Pro'
```

预期：两端 `CoreProcessor` 测试通过；Swift 未直接引用 C++ 头文件。

## 任务 5：以红测实现共享诊断 view model 与 SwiftUI 页面

**文件：**
- 新建：`apple/App/Features/Diagnostics/CoreDiagnosticsViewModel.swift`
- 新建：`apple/App/Features/Diagnostics/CoreDiagnosticsView.swift`
- 新建：`apple/AppTests-macOS/CoreDiagnosticsViewModelTests.swift`
- 新建：`apple/AppTests-iOS/CoreDiagnosticsViewModelTests.swift`
- 修改：`apple/App/JKMeetingApp.swift`

**步骤：**

1. 在 macOS 测试 target 先写 `CoreDiagnosticsViewModelTests`：以确定性 `CoreProcessor` 输出驱动 view model，断言展示文本包含 `2 Hz`、`1 ch` 和 `2 samples`。
2. 运行测试，确认因 `CoreDiagnosticsViewModel` 尚不存在而失败。
3. 以最小实现新增无 UI 依赖的 `CoreDiagnosticsViewModel`：只负责发起确定性 core 自检、保存成功 metadata 或用户可读错误。
4. 新增共享 `CoreDiagnosticsView`，仅显示“运行 core 自检”按钮、成功 metadata 或错误文本；不得访问麦克风、文件或网络。
5. 在 iOS 独立测试 target 加入同等 view model 测试。
6. 分别运行 macOS 与 iOS Simulator 测试和 App 构建。

**验证：**

```sh
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'platform=iOS Simulator,name=iPhone 17 Pro'
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'platform=iOS Simulator,name=iPhone 17 Pro'
```

预期：共享 UI 在两个 target 均可构建；所有确定性 PCM 测试通过。

## 任务 6：接入统一验证并记录构建证据

**文件：**
- 修改：`Makefile`
- 修改：`apple/README.md`
- 修改：`docs/metrics.md`
- 修改：`docs/plans/2026-09-29-p0-minimal-loop-design.md`

**步骤：**

1. 新增 `build-apple-macos`、`test-apple-macos`、`test-apple-ios-simulator`、`build-apple-ios-simulator` 和 `verify-apple-project` Makefile 目标。
2. `verify-apple-project` 必须从仓库根目录运行以下命令；Makefile recipe 不得通过 `cd apple` 改变 Git diff 的相对路径：

```sh
xcodegen generate --spec apple/project.yml
git diff --exit-code -- apple/JKMeeting.xcodeproj
```

以阻止生成工程漂移。
3. 仅在所有 Apple 命令本机成功后，才将它们纳入 `make verify`。
4. 更新 `apple/README.md`：说明 Xcode / CMake 边界、两端 target、验证命令和真实麦克风验证尚未完成。
5. 在 `docs/metrics.md` 写入构建证据：日期、Xcode 版本、target、命令、结果与原始输出位置。
6. 更新 P0 设计的测试矩阵，明确 Simulator 测试不能替代 iPhone / Mac 真实录音 smoke 测试。

**验证：**

```sh
make test-core
make test-apple-bridge
make verify-apple-project
make build-apple-macos
make test-apple-macos
make build-apple-ios-simulator
make test-apple-ios-simulator
make verify
git diff --check
```

预期：core、bridge、两端单测和两端构建均通过；`xcodegen generate` 不产生未提交工程漂移；文档和代码改动无空白错误。

## 延后事项

后续独立需求再引入 `AVAudioEngine`：先为平台无关采集协议和状态机写测试，再分别接入 iOS / macOS 平台适配器，最后用 iPhone 与 Mac 的真实麦克风完成录音、core、落盘和回放 smoke 测试。上传、ASR 和网络仍不属于本计划。
