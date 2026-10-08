# P0 录音服务实施计划

> **执行要求：**仅在独立评审结论为“通过”后执行代码任务；每一项行为先写失败测试，再做最小实现。未经用户明确要求不提交、推送或发布。

**目标：**实现支持 S3 预签名上传的 Go 录音服务，保存录音状态到 PostgreSQL，并在上传确认后投递 Redis 转写任务。

**架构：**单一 Go API 进程通过 pgx 访问 PostgreSQL、通过 S3 客户端访问 SeaweedFS、通过 asynq 投递 Redis 任务。音频字节始终在客户端与 SeaweedFS 间直传；API 只保存元数据、验证对象存在性、生成预签名 URL 与迁移状态。真实 ASR / Worker 消费不进入本切片。

**技术栈：**Go 1.26、net/http、pgx v5、minio-go v7（S3 兼容）、asynq、PostgreSQL 16、Redis 7、SeaweedFS 3.68、Docker Compose。

---

## 评审关注点

- SeaweedFS 只通过标准 S3 兼容接口访问，不在 Go 业务代码中使用私有 API。
- `complete` 必须先验证对象存在，再使用条件更新保证状态迁移幂等。
- HTTP handler 不处理音频 body；上传必须通过预签名 PUT URL 直接发生。
- `created → uploaded` 与 outbox 记录插入必须位于同一 PostgreSQL 事务；asynq 投递仅消费 outbox，避免进程崩溃导致“已上传但无任务”。
- 本轮只验证 outbox 与 asynq 任务投递，不启动 Worker / whisper.cpp。
- 所有 API 请求必须生成或透传 `request_id`，并写入结构化中文日志与任务 payload。

## 任务 1：初始化 Go 模块与配置边界

**文件：**
- 新建：`server/go.mod`
- 新建：`server/cmd/api/main.go`
- 新建：`server/internal/config/config.go`
- 新建：`server/internal/config/config_test.go`

**步骤：**

1. 写失败测试：缺少 `DATABASE_URL`、`REDIS_ADDR`、`S3_ENDPOINT`、`API_BEARER_TOKEN` 时 `Load()` 返回错误。
2. 运行：`go test ./internal/config`，预期因包不存在而失败。
3. 实现最小配置加载；本地默认 endpoint 为 `http://127.0.0.1:9000`，bucket 为 `recordings`，其余敏感值必须通过环境变量传入。
4. 重新运行配置测试。

## 任务 2：创建 migration 与录音 repository

**文件：**
- 新建：`server/migrations/0001_create_recordings.sql`
- 新建：`server/internal/recording/model.go`
- 新建：`server/internal/recording/repository.go`
- 新建：`server/internal/recording/repository_test.go`
- 新建：`server/internal/outbox/repository.go`
- 新建：`server/internal/outbox/repository_test.go`

**步骤：**

1. 写失败集成测试：创建记录初始状态为 `created`；仅允许 `created → uploaded` 条件更新；任意非法状态迁移返回未更新；迁移成功时同一事务新增一条未投递 outbox 记录。
2. 使用 `docker exec` 对 PostgreSQL 应用 migration，运行测试前确认失败。
3. 创建 `recordings` 表：UUID 主键、状态 CHECK（`created` / `uploaded` / `transcribing` / `done` / `failed`）、对象键、时长、大小、content type、文本、错误、时间字段及 `(user_id, created_at DESC)` 索引；同时创建 `transcription_outbox` 表，保存 recording id、确定性 task id、request id、投递状态与时间字段。
4. 实现 repository 的 `Create`、`MarkUploadedAndCreateOutbox`、`FindByID`、`MarkFailedIfUploaded`；所有状态更新使用 `WHERE status = ...` 条件。
5. 运行 recording / outbox 集成测试。

## 任务 3：实现 S3 预签名与对象存在检查

**文件：**
- 新建：`server/internal/storage/s3.go`
- 新建：`server/internal/storage/s3_test.go`

**步骤：**

1. 写失败测试：为固定 object key 生成 PUT / GET URL，且 URL 包含正确 bucket / key；不存在对象的 `Exists` 返回 false。
2. 使用 SeaweedFS endpoint 和本地开发凭证运行测试，预期因实现缺失失败。
3. 实现 S3 客户端创建、bucket 确保存在、预签名 PUT / GET、`StatObject` 存在检查。
4. 运行 storage 测试；验证 URL 指向 `127.0.0.1:9000`。

## 任务 4：实现创建与上传确认 API

**文件：**
- 新建：`server/internal/httpapi/recording_handler.go`
- 新建：`server/internal/httpapi/recording_handler_test.go`
- 新建：`server/internal/httpapi/request_id.go`
- 新建：`server/internal/queue/transcription.go`
- 新建：`server/internal/queue/outbox_dispatcher.go`

**步骤：**

1. 写失败 HTTP 测试：
   - 未带固定 Bearer token 返回 401；
   - `POST /v1/recordings` 只接受 `{duration_ms, size_bytes, content_type}`，非法时长、负大小和空 content type 返回 400；身份固定映射为 `p0-local-user`；
   - 创建成功返回 201、recording id、PUT URL、到期时间和 request id；
   - 不存在对象调用 complete 返回 409；
   - 已存在对象 complete 后返回 `uploaded`，并在事务内生成 outbox；dispatcher 用确定性 task id 投递 asynq 后标记 outbox 已投递；
   - 请求日志和任务 payload 都包含 request id。
2. 运行 handler 测试，预期因 handler / queue 不存在失败。
3. 实现固定 Bearer middleware、request-id middleware、结构化中文日志、JSON 解码、创建记录、预签名 URL、SeaweedFS 对象检查、条件状态迁移、outbox 事务和 asynq dispatcher；dispatcher 必须通过 `UPDATE ... WHERE status='pending' RETURNING ...` 原子认领一条 outbox 记录，以确定性 task id 投递，投递成功后仅对认领记录条件标记为 `dispatched`。task payload 至少包含 `recording_id` 与 `request_id`。
4. 重新运行 HTTP 测试。

## 任务 5：实现查询与回放 API

**文件：**
- 修改：`server/internal/httpapi/recording_handler.go`
- 修改：`server/internal/httpapi/recording_handler_test.go`

**步骤：**

1. 写失败测试：
   - `GET /v1/recordings/{id}` 返回状态、时长、文本和错误字段；
   - `GET /v1/recordings/{id}/playback` 仅在对象存在时返回 GET URL。
2. 运行测试，预期 404 或未实现失败。
3. 实现查询、回放 URL 生成与不存在资源的 404 映射。
4. 重新运行 handler 测试。

## 任务 6：提供可复现启动与 curl 冒烟入口

**文件：**
- 新建：`server/scripts/migrate.sh`
- 新建：`server/scripts/smoke.sh`
- 修改：`Makefile`
- 新建：`server/README.md`
- 修改：`docs/metrics.md`

**步骤：**

1. 添加 `make migrate-server`、`make test-server`、`make smoke-server`；不得使用占位实现。
2. 冒烟脚本依次执行：创建录音 → PUT 一段小测试文件到预签名 URL → complete → GET 状态 → GET playback。
3. 运行全部 server 测试与冒烟脚本。
4. 在 `docs/metrics.md` 写入环境、命令、状态与耗时；注明 ASR / Worker 消费仍未实现。

## 验收命令

```sh
docker compose up -d
make migrate-server
make test-server
make smoke-server
git diff --check
```

预期：migration、单元 / 集成测试与 curl 冒烟全部通过；音频字节未经过 Go API；记录在 complete 后为 `uploaded`；Redis 存在转写任务。
