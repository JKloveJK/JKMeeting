# P0 异步转写实施计划

> **执行要求：**独立评审通过后再写 Worker 业务代码。每个状态迁移、重试与临时文件清理行为均先有失败测试。未经用户明确要求不提交、推送或发布。

**目标：**消费 Redis 转写任务、调用宿主机 whisper.cpp server、将录音状态可靠地从 `uploaded` 推进到 `done` 或 `failed`。

**架构：**Go Worker 与 API 使用同一 PostgreSQL、SeaweedFS、Redis 配置。首次任务以条件更新认领 `uploaded → transcribing`，生成随机 lease token、设置 lease 到期时间并递增尝试次数；临时 ASR 失败时仅匹配当前 token 条件更新回 `uploaded`，以允许下一次 asynq 重试。Worker 崩溃遗留的过期 lease 可由后续任务生成新 token 重新认领；旧 Worker 的释放、`done`、`failed` 更新都因 token 不匹配而失效。总尝试次数固定为 3（asynq `MaxRetry(2)`）；第 3 次失败由 handler 以 token 条件更新为 `failed` 与脱敏错误信息。成功时同样以 token 条件更新 `transcribing → done` 并写入 transcript。whisper.cpp 在宿主机运行，避免 Docker 访问 Metal 的限制。

**技术栈：**whisper.cpp 1.9.4、base GGML 模型、Metal、Go 1.26、asynq、pgx v5、minio-go v7、PostgreSQL、Redis、SeaweedFS。

---

## 任务 1：准备并验证本机 whisper.cpp

**文件：**
- 新建：`server/scripts/start-whisper.sh`
- 新建：`server/scripts/download-whisper-model.sh`
- 新建：`server/whisper/README.md`

**步骤：**

1. 安装并锁定 Homebrew `whisper-cpp` 1.9.4：使用 `HOMEBREW_NO_AUTO_UPDATE=1 brew install -y whisper-cpp`；脚本通过 `command -v whisper-server` 与 `whisper-server --version` 记录实际二进制路径与版本，版本不符时明确失败。
2. 下载 base GGML 模型到 `server/whisper/models/`；脚本固定模型 URL、期望 SHA256，下载后使用 `shasum -a 256 -c` 校验，校验不一致时拒绝启动。
3. 启动实际探测到的 `whisper-server`，绑定 `127.0.0.1:8081`，参数包含模型路径、`--host 127.0.0.1`、`--port 8081`、`--convert`；记录 Metal 是否启用，且不传 `--no-gpu`。
4. ASR HTTP 契约固定为：`POST /inference`，`multipart/form-data` 的 `file` 字段、`response_format=json`、`language=auto`；成功 JSON 读取 `text` 字段。连接超时 5 秒、整体超时 120 秒，单文件上限 32 MB，非 2xx / 空文本均为可重试错误。`request_id` 写入 HTTP Header 与 Worker 日志。
5. 用固定 WAV 测试样本调用 `/inference`，记录 HTTP 状态、耗时和输出格式。

**验收：**`curl -F file=@<sample.wav> -F response_format=json http://127.0.0.1:8081/inference` 返回含非空 `text` 的 JSON，模型加载与 Metal 配置在日志可见。

## 任务 2：扩展录音状态与 Worker repository

**文件：**
- 修改：`server/internal/recording/repository.go`
- 修改：`server/internal/recording/repository_test.go`
- 新建：`server/internal/transcribe/repository.go`
- 新建：`server/internal/transcribe/repository_test.go`

**步骤：**

1. 写失败测试：仅允许 `uploaded → transcribing`、`transcribing → done`、`transcribing → failed`；临时失败可条件释放为 `uploaded`；过期 lease 的 `transcribing` 可重新认领；重复任务或已完成任务不得改写结果。
2. migration 增加 `transcribe_attempts`、`transcribe_lease_until`；实现条件更新与 `RowsAffected == 1` 语义。
3. Worker 认领失败且记录已是 `done` / `failed` 时直接返回 nil；认领失败但 lease 未过期时返回可重试错误，避免静默丢失任务。

## 任务 3：实现对象下载与 ASR client

**文件：**
- 新建：`server/internal/transcribe/downloader.go`
- 新建：`server/internal/transcribe/downloader_test.go`
- 新建：`server/internal/transcribe/client.go`
- 新建：`server/internal/transcribe/client_test.go`

**步骤：**

1. 写失败测试：下载成功返回临时文件路径；函数返回前失败时也清理文件；object key 仅作为 S3 key，不参与本地路径拼接。
2. 写失败测试：ASR HTTP 非 2xx、空 transcript、超时或超过 32 MB 返回可重试错误。
3. 使用 `os.CreateTemp` 创建受控 0600 临时文件，严格关闭和 `defer os.Remove`；实现标准 S3 下载、multipart HTTP 调用与 JSON `text` 解析。

## 任务 4：实现 asynq Worker handler 与重试

**文件：**
- 新建：`server/internal/transcribe/handler.go`
- 新建：`server/internal/transcribe/handler_test.go`
- 新建：`server/cmd/worker/main.go`

**步骤：**

1. 写失败测试：成功路径写入 transcript / `done`；临时 ASR 错误释放 lease 并返回 asynq retry；第 3 次失败条件写入 `failed`；崩溃遗留过期 lease 可重新认领。
2. 实现 handler，解析 `recording_id` / `request_id`，记录 `recording_id`、`request_id`、task id、尝试次数、下载耗时与 ASR 耗时的结构化中文日志。
3. 固定总尝试次数为 3：asynq `MaxRetry(2)`；错误信息限制长度、脱敏后写入 `error_message`，日志保留原始根因。
4. Worker 启动时连接 Redis，注册 `transcription:run` handler。

## 任务 5：端到端转写与指标

**文件：**
- 新建：`server/scripts/smoke-asr.sh`
- 修改：`Makefile`
- 修改：`server/README.md`
- 修改：`docs/metrics.md`

**步骤：**

1. 新增 `make run-worker`、`make run-whisper`、`make smoke-asr`。
2. 冒烟：上传固定音频 → complete → 轮询查询直到 `done` / `failed`。
3. 记录队列等待、尝试次数、对象下载耗时、ASR 耗时、音频时长、RTF（音频时长 / ASR 耗时）、模型、机器与原始命令；首次和稳态测量分别标记。

**验收命令：**

```sh
make migrate-server
make test-server
make run-whisper
make run-worker
make smoke-asr
```

预期：固定测试音频最终为 `done`，查询接口返回 transcript；ASR 故障注入最终为 `failed`。
