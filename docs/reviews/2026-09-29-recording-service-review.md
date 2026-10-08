# P0 录音服务技术方案评审

- 日期：2026-09-29
- 评审角色：独立评审 agent
- 关联需求：`docs/requirements/2026-09-29-recording-service-requirements.md`
- 关联方案：`docs/plans/2026-09-29-recording-service-implementation.md`
- 评审范围：接口契约、状态机、数据库事务、S3、Redis 任务、错误处理与可测试性。

## 第一阶段：问题清单

| 级别 | 问题 | 要求的修订 |
| --- | --- | --- |
| 阻塞 | `created → uploaded` 与 asynq 投递无原子性 / 补偿机制，可能出现状态已上传但无任务 | 使用事务 outbox：状态迁移与 outbox 插入同一事务，dispatcher 使用确定性 task id 投递 |
| 高 | 状态集合、失败迁移及条件 SQL 验证不完整 | 添加状态 CHECK、完整迁移定义、每次状态更新 `WHERE status=...` 测试 |
| 高 | 仅定义 request_id payload，未定义 API 日志 | 添加 request-id middleware、结构化中文日志与测试 |
| 中 | 未定义 `user_id` 来源、创建请求字段和校验 | 固定映射 `p0-local-user`，明确 JSON schema 与 4xx 用例 |

## 方案修订核对

| 项目 | 核对结果 | 依据 |
| --- | --- | --- |
| 事务 outbox | 部分通过 | `created → uploaded` 与 outbox 插入已在同一事务；但 dispatcher 未定义 `WHERE status='pending'` 的原子认领与投递后条件标记 |
| 完整状态与条件迁移 | 通过 | 状态 CHECK 完整，状态更新明确使用 `WHERE status=...` |
| request_id 日志 | 通过 | 已定义生成 / 透传、结构化中文日志和 task payload |
| schema / 固定用户 | 通过 | 已定义创建字段、校验和 `p0-local-user` |

## 结论

**不通过。**

本轮唯一对抗评审闭环结束后，仍有一个阻塞项：outbox dispatcher 必须以条件更新原子认领 pending 记录，投递成功后再以条件更新标记已投递，避免并发 dispatcher 重复投递或覆盖状态。

## 用户决策

用户于 2026-09-29 确认：允许直接补齐 outbox dispatcher 的原子认领、确定性 task id 与投递后条件标记，并开始实现；不再追加评审。本记录保留“不通过”作为独立评审结论的事实，后续编码依据用户明确授权推进。
