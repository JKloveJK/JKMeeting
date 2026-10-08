# Apple 双端录音与回放独立对抗评审

- 日期：2026-09-29
- 需求：`docs/requirements/2026-09-29-apple-recording-playback-requirements.md`
- 方案：`docs/plans/2026-09-29-apple-recording-playback-implementation.md`
- 评审模式：同一独立评审 agent 的一次闭环

## 初审问题

| 级别 | 问题 | 修订 |
| --- | --- | --- |
| 阻塞 | iOS 使用说明与 macOS `audio-input` entitlement 未明确 | 已补 `NSMicrophoneUsageDescription`、sandbox 与 `com.apple.security.device.audio-input=true` |
| 高 | 中断语义、tap 背压/线程、PCM 到 AAC/m4a 转换未定义 | 已补有效文件回读、队列丢帧阈值、串行 core/writer、`AVAudioConverter` 与 `AVAudioFile` 回读 |
| 中 | 平台事件、真机证据和指标不足 | 已补 iOS/macOS 事件、双端 smoke 与指标字段 |
| 中 | Task 5 越出本切片 | 已移除 server/ASR 验证 |

## 修订核对

| 项目 | 结果 | 依据 |
| --- | --- | --- |
| 权限和 entitlement | 通过 | 方案已明确 iOS 和 macOS 的工程前置项 |
| tap、背压与 core 串行 | 通过 | 已定义有界队列、丢帧阈值和单一处理队列 |
| AAC/m4a 写入与回读 | 通过 | 已定义 `AVAudioConverter`、m4a 与 `AVAudioFile` 校验 |
| 平台事件、真机证据、P0 边界 | 通过 | 已覆盖相关平台事件、指标与非目标 |
| 中断状态语义 | 不通过 | 修订说明的“有效文件转 `recorded`”与任务 1 的“一律 `failed`”冲突 |
| 5 分钟自动停止 | 不通过 | 未明确自动 stop 后的文件关闭、回读、最终状态与用户提示 |

## 结论

**不通过。**在方案中统一中断状态规则，并补齐 5 分钟触顶时“停止采集 → 关闭 writer → 回读校验 → `recorded` 或 `failed` → 用户提示”的状态与验收后，才可进入编码。
