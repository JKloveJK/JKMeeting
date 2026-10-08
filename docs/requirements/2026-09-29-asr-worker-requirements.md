# P0 异步转写需求

- 日期：2026-09-29
- 状态：已确认
- 关联：`docs/plans/2026-09-29-p0-minimal-loop-design.md`、`docs/requirements/2026-09-29-recording-service-requirements.md`

## 背景与问题

录音服务已能创建记录、直传对象、写入 outbox 并向 Redis 投递转写任务，但任务尚未被消费，录音状态停留在 `uploaded`。P0 需要完成本机 ASR 闭环，用户才能在查询接口获得文本或明确失败原因。

## 目标

1. 在宿主机启动 `whisper.cpp` HTTP server，使用 Metal 加速的 base 模型。
2. 启动 asynq Worker 消费 `transcription:run` 任务。
3. Worker 下载 SeaweedFS 中的音频对象，调用本机 ASR server，将状态更新为 `transcribing → done` 并保存 transcript。
4. 临时失败最多重试 3 次；耗尽后更新为 `failed` 与用户可读 `error_message`。
5. 以 curl / 脚本验证从上传确认到查询 transcript 的完整异步闭环，并记录首次 RTF。

## 范围

- whisper.cpp Homebrew 安装、base 模型下载、本机 HTTP server 启动脚本。
- Go Worker、asynq handler、ASR HTTP client、状态条件迁移、失败重试。
- S3 对象下载到 Worker 临时目录，转写后删除临时文件。
- `request_id` 贯穿任务日志、ASR 调用和状态更新。

## 非目标

- 不做流式转写、说话人分离、摘要、翻译、多语言模型选择或云 ASR。
- 不做 Worker 横向扩缩容、死信队列、任务优先级或完整观测平台。
- 不改变客户端上传协议、S3 对象存储协议或 C++ core。

## 验收标准

| 编号 | 判据 | 证据 |
| --- | --- | --- |
| A1 | `whisper-server` 可在宿主机响应健康 / 转写请求 | 本机 HTTP 请求与启动日志 |
| A2 | Worker 消费 Redis 中 `transcription:run` 任务 | Worker 日志含 recording id / request id |
| A3 | 成功任务将状态迁移为 `done` 且写入非空 transcript | 查询接口与 PostgreSQL 记录 |
| A4 | ASR 不可达时任务最多尝试 3 次，最终为 `failed` 并有错误信息 | handler 测试与数据库状态 |
| A5 | 临时下载文件在任务结束后删除 | 单测或临时目录检查 |
| A6 | 记录 30 秒或固定测试音频的首次 ASR RTF | `docs/metrics.md` 记录环境、模型、音频与耗时 |

## 约束与依赖

- 本机已验证 PostgreSQL、Redis、SeaweedFS healthy。
- `whisper.cpp` 当前未安装；Homebrew 公式为 `whisper-cpp`，模型文件需单独下载。
- ASR server 必须运行在宿主机，Docker 中 Worker 通过 `host.docker.internal` 访问；本轮 Go Worker 也支持本机直连地址。
- 状态更新必须条件更新；重复任务不得覆盖 `done` 或重复生成结果。

## 待确认问题

无。P0 使用 base 模型，模型与服务端口由实施方案固定。
