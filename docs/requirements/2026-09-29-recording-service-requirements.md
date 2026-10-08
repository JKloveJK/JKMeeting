# P0 录音服务需求

- 日期：2026-09-29
- 状态：已确认
- 关联：`docs/plans/2026-09-29-p0-minimal-loop-design.md`、`docs/decisions/0004-local-s3-compatible-storage.md`

## 背景与问题

Apple 客户端已具备 C++ core、ObjC++ bridge 与可运行工程。P0 下一步需要建立本地服务端主链路，使录音文件可在客户端与 S3 兼容对象存储之间直接传输，并将转写作为可查询、可重试的异步任务管理。

## 目标

1. 创建 PostgreSQL `recordings` 表及状态迁移约束。
2. 提供创建录音、上传确认、查询录音、获取回放 URL 四个 HTTP 接口。
3. 服务端通过 SeaweedFS 的 S3 兼容 API 生成预签名 PUT / GET URL，不代理音频字节。
4. 上传确认后在同一 PostgreSQL 事务中创建 outbox 记录；dispatcher 再将转写任务投递 Redis，以避免状态已更新但任务丢失；本轮先验证任务投递，不接入 whisper.cpp。
5. 通过 curl 完成“创建 → 直接上传 → 确认 → 查询”冒烟链路。

## 范围

- Go 单体 API 进程；数据库 migration；S3 兼容对象存储客户端；Redis 任务投递。
- P0 固定 Bearer token 鉴权。
- `request_id` 写入 API 日志与任务 payload。
- SeaweedFS bucket：`recordings`。

## 非目标

- 不接入真实 ASR、Worker 消费、重试策略和转写文本生成。
- 不实现用户注册、多人权限、HTTPS、云部署、后台音频或实时流。
- 不引入 API 网关、微服务拆分、完整指标平台或 OpenTelemetry。

## 验收标准

| 编号 | 判据 | 证据 |
| --- | --- | --- |
| R1 | migration 创建 `recordings` 表与查询索引 | PostgreSQL 查询表结构 |
| R2 | 创建接口返回 `recording_id`、预签名 PUT URL 和到期时间 | Go HTTP 测试与 curl 输出 |
| R3 | 客户端可将测试对象直接 PUT 到 SeaweedFS | curl PUT 返回成功，API 不接收音频 body |
| R4 | complete 接口仅在对象存在时将 `created` 迁移为 `uploaded`，同一事务创建 outbox；dispatcher 以确定性 task id 投递 Redis 后标记 outbox | HTTP 测试、事务测试、数据库状态、Redis 队列记录 |
| R5 | 查询接口返回 `uploaded` / `transcribing` / `failed` 等实际状态与可选错误文本 | Go HTTP 测试 |
| R6 | 回放接口仅对已存在对象返回预签名 GET URL | Go HTTP 测试 |
| R7 | curl 冒烟脚本成功跑通创建、上传、确认与查询 | 脚本输出与退出码 0 |

## 约束与依赖

- PostgreSQL、Redis、SeaweedFS Docker healthcheck 均已通过。
- 使用 SeaweedFS `http://127.0.0.1:9000` 作为本地 S3 endpoint，使用 `server/seaweedfs/s3.json` 中的开发凭证。
- SQL 状态迁移必须使用条件更新，禁止“先读后改”。
- 所有服务端文档和日志说明使用简体中文；API 字段、代码与命令保持原始拼写。

## 待确认问题

无。真实 ASR 与 Worker 消费将在下一项 P0 子需求中完成。
