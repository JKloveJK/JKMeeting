# Apple 双端录音与回放需求

- 日期：2026-09-29
- 阶段：P0 第 5 步
- 状态：待方案评审
- 关联：`docs/plans/2026-09-29-p0-minimal-loop-design.md`、ADR 0003

## 1. 目标

在既有 iOS/macOS SwiftUI 工程中，分别完成前台真实麦克风录音、PCM 经 `CoreProcessor` 处理、原生 AAC/m4a 本地落盘与本地回放。两端均为一等客户端，复用 SwiftUI 状态、Swift facade、ObjC++ bridge 和 C++ core；平台差异只留在权限、音频会话、文件与播放壳层。

## 2. 范围

1. 请求并展示麦克风权限状态；拒绝时给出明确设置引导。
2. 使用 `AVAudioEngine` tap 获取 PCM；采集层将合法 PCM 交给 `CoreProcessor`。
3. 使用 Apple 原生音频 API 写入 AAC/m4a 文件，记录录音时长、采样率、声道数与本地路径。
4. 使用本地播放控制器播放、停止和重新播放该文件。
5. 建立共享录音状态机：`idle`、`requestingPermission`、`recording`、`stopping`、`recorded`、`playing`、`failed`。
6. iOS 与 macOS 均有独立 XCTest；macOS 本机和 iPhone 真机完成真实麦克风 smoke。

## 3. 非目标

- 不接入 Go API、上传、转写轮询、ASR 文本展示或远程回放。
- 不做后台录音、断点续传、边录边传、设备选择、菜单栏、全局快捷键或 Mac 专属能力。
- 不把 AVFoundation、文件 IO、权限或播放代码放入 C++ core。
- Simulator 构建或确定性 PCM 测试不作为真实麦克风验收替代。

## 4. 平台边界

| 能力 | iOS | macOS | 共享层 |
| --- | --- | --- | --- |
| 权限 | `AVAudioSession` 与系统授权 | `AVCaptureDevice` 授权 | 权限结果映射与状态机 |
| 采集 | `AVAudioEngine` + app 音频会话 | `AVAudioEngine` | PCM 编排、`CoreProcessor` 调用 |
| 落盘/播放 | AVFoundation 原生文件与播放器 | AVFoundation 原生文件与播放器 | 文件元数据与用户可见状态 |

## 5. 错误可见性

- 权限拒绝、音频引擎启动失败、音频会话中断、core/bridge 错误、落盘失败和播放失败必须映射为用户可见状态与可诊断日志。
- 录音被中断时保留已成功写入的文件；未形成有效文件时不得伪造成功记录。
- 失败不静默降级，不自动上传、不自动重录。

## 6. 验收

| 编号 | 验收标准 | 证据 |
| --- | --- | --- |
| R1 | iOS/macOS target 和独立 XCTest 均可构建 | `xcodebuild` 输出 |
| R2 | 确定性 PCM 经 `CoreProcessor` 后 metadata 正确 | 两端单测 |
| R3 | 权限拒绝、引擎启动失败与中断状态可测试 | 共享状态机单测 |
| R4 | Mac 本机可录制短音频、生成可播放 m4a 并回放 | 手工 smoke 与日志 |
| R5 | iPhone 真机可完成同一链路 | 真机 smoke 与日志 |
| R6 | C++ core 不出现 Apple SDK、文件或 UI 依赖 | 代码审查与现有 CTest |
