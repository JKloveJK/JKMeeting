# P0 异步转写技术方案评审

- 日期：2026-09-29
- 评审角色：独立评审 agent
- 关联需求：`docs/requirements/2026-09-29-asr-worker-requirements.md`
- 关联方案：`docs/plans/2026-09-29-asr-worker-implementation.md`
- 评审范围：ASR 安装、状态机、重试、lease、临时文件、错误处理、指标。

## 第一阶段：问题清单

| 级别 | 问题 | 要求的修订 |
| --- | --- | --- |
| 阻塞 | 首次转为 `transcribing` 后重试无法重新认领，任务会被误判成功 | 引入受控 lease、attempt，并设计临时失败释放与 Worker 崩溃恢复 |
| 阻塞 | asynq 重试次数与“总共 3 次”语义不明确；最终失败写入未定义 | 明确 `MaxRetry(2)`、第 3 次条件写入 `failed` |
| 高 | ASR HTTP 契约和安装 / 模型复现信息不足 | 固定 `/inference`、multipart 字段、超时、模型 URL/SHA、启动参数和 Metal 验证 |
| 高 | 临时文件与 object key 路径安全不足 | 使用 CreateTemp、0600、大小上限、关闭删除、禁止 key 拼路径 |
| 中 | 错误映射与指标口径不足 | 定义脱敏错误、错误码、队列/下载/ASR 耗时与 RTF 口径 |

## 方案修订核对

| 项目 | 核对结果 | 依据 |
| --- | --- | --- |
| 重试 / lease | 未通过 | 已增加 lease 与释放设计，但未定义 lease token；旧执行者在 lease 过期后仍可能覆盖新执行者结果 |
| 总尝试次数 | 通过 | `MaxRetry(2)` 明确为总共 3 次 |
| ASR HTTP 契约 | 通过 | 路径、字段、响应、超时、大小、错误语义与 request id 已固定 |
| 安装 / 模型复现 | 未通过 | Homebrew 未锁定版本；`--convert` 依赖与 `.m4a` 转写未作为验收 |
| 临时文件 | 通过 | CreateTemp、0600、受控路径、关闭删除与 key 隔离均已覆盖 |
| 错误脱敏 / 指标 | 基本通过 | 已限制长度和补充指标；仍应定义脱敏规则与错误码 |

## 结论

**不通过。**

本轮唯一对抗评审闭环结束后，存在两个阻塞项：

1. migration 必须增加 lease token；重新认领时生成新 token，释放 / `done` / `failed` 的条件更新都必须同时匹配 token，避免旧 Worker 覆盖新 Worker 的结果。
2. 安装脚本必须锁定 whisper.cpp 版本和模型 SHA，并以实际 `.m4a` 文件验证 `--convert` 转换链路。

## 用户决策

用户于 2026-09-29 确认：允许直接补齐 lease token fencing 与 whisper 安装 / 模型校验 / `.m4a` 验证，不再追加评审。本记录保留“不通过”作为独立评审结论的事实；后续 Worker / ASR 编码依据用户明确授权推进。
