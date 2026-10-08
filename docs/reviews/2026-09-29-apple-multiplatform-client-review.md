# Apple 双端客户端技术方案评审

- 日期：2026-09-29
- 评审角色：独立评审 agent
- 关联需求：`docs/requirements/2026-09-29-apple-multiplatform-client-requirements.md`
- 关联方案：`docs/plans/2026-09-29-apple-multiplatform-client-implementation.md`
- 关联 ADR：`docs/decisions/0003-apple-multiplatform-shell.md`
- 评审范围：需求覆盖、架构边界、风险、可测试性、实施步骤

## 第一阶段：问题清单

| 级别 | 问题 | 证据或定位 | 要求的修订 |
| --- | --- | --- | --- |
| 阻塞 | facade 红测先于工程、Scheme 与测试 target 创建 | 原方案任务 2 在任务 3 前执行 `xcodebuild test` | 先生成工程和测试 target，再写 facade 红测 |
| 阻塞 | iOS 与 macOS XCTest target 的 bundle、source membership、Scheme test action 未定义 | 原方案仅描述“单元测试 bundle” | 为两个平台分别定义测试 target 与 Scheme |
| 高 | Xcode 与 CMake 的编译 / 链接边界不明确 | 原方案只写“直接链接 C++ 实现” | Xcode 直接编译 `.mm + .cpp`；CMake 仅独立验证；禁止链接 CMake 产物 |
| 阻塞 | P0 双端范围与 Apple 壳层边界在实施后才记录 | 原方案在最后任务才创建 ADR | 先更新 P0 设计并创建 ADR，再开始编码 |
| 高 | bridge 错误 domain / code 仅在 `.mm` 私有定义 | Swift facade 无稳定错误映射依据 | 在 header 公开稳定错误契约 |
| 中 | 无行为的目录迁移未说明 TDD 例外与迁移前后验证 | 原方案任务 1 | 迁移前后分别运行 bridge 回归测试 |
| 中 | 诊断 view model 未列入文件清单和测试职责 | 原方案 UI 任务 | 独立定义 view model、红测与 View |
| 中 | `project.yml` 与 `.xcodeproj` 可能漂移 | 原方案未检查生成结果 | `xcodegen generate` 后使用 Git diff 检查 |

## 方案修订核对

| 问题 | 修订位置 | 核对结果 |
| --- | --- | --- |
| 工程 / 红测顺序 | 技术方案“任务 3”先生成工程，“任务 4”再写 facade 红测 | 已解决 |
| 双平台独立 XCTest | 技术方案“任务 3、4、5”规定 iOS / macOS 独立 bundle 和 source membership | 未解决：未明确测试 target 对 App target 的依赖与 `@testable import` |
| Xcode / CMake 边界 | ADR 0003、技术方案“架构”和“任务 3” | 已解决 |
| P0 双端范围前置记录 | 需求文档、ADR 0003、P0 设计 | 已解决 |
| 公开 bridge 错误契约 | 技术方案“任务 4”、ADR 0003 | 已解决 |
| 迁移回归验证 | 技术方案“任务 2” | 已解决 |
| 独立诊断 view model | 技术方案“任务 5” | 已解决 |
| XcodeGen 漂移检查 | 技术方案“任务 6” | 未解决：Git diff 路径未明确为仓库根目录下的 `apple/JKMeeting.xcodeproj` |
| Swift 调用 ObjC bridge | 技术方案“任务 3、4” | 未解决：未定义 bridging header 与 `SWIFT_OBJC_BRIDGING_HEADER`，Swift 无法识别 `JKMeetingCore` |

## 结论

**不通过。**

本次唯一对抗评审闭环结束后，仍存在以下阻塞项：

1. 必须在 `project.yml` 中定义 `JKMeeting-Bridging-Header.h` 与 `SWIFT_OBJC_BRIDGING_HEADER`，将 `JKMeetingCore.h` 暴露给 Swift。
2. 必须明确 iOS / macOS XCTest target 对各自 App target 的依赖、`@testable import` 模块名以及 Scheme 的测试动作。
3. `verify-apple-project` 必须从仓库根目录执行 `git diff --exit-code -- apple/JKMeeting.xcodeproj`。

根据 `AGENTS.md` 的“一轮对抗评审”规则，未通过时停止编码并由用户决定后续处理；当前不得执行目录重命名、Xcode 工程创建或 Swift 代码实现。

## 若获准继续时必须复验的风险

- XcodeGen 生成的 iOS / macOS target 均正确编译 `.mm` / `.cpp`，启用 ARC 与 C++20。
- macOS 与 iOS Simulator 的 XCTest bundle 均可独立执行。
- `xcodegen generate` 后不存在工程漂移。
- Xcode 未链接 CMake 产物，避免重复符号。
- 构建和确定性 PCM 测试不得被表述为真实麦克风验证。

## 用户决策

用户于 2026-09-29 明确授权：由方案 agent 补齐剩余 3 个工程配置项后直接开始实施，不再发起额外评审。本记录保留“不通过”结论作为当时的独立评审事实；后续编码依据用户的明确例外授权推进，仍必须完成计划规定的测试和最终用户终审。
