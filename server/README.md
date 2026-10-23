# P0 录音服务

本模块是 JKMeeting 的 P0 Go 服务端。它不代理音频字节，而是保存录音元数据、生成 S3 预签名 URL、确认上传并投递异步转写任务。

## 边界

- PostgreSQL 保存录音状态、文本和错误信息。
- SeaweedFS 仅通过 S3 兼容 API 保存音频对象。
- Redis / asynq 保存转写任务（`default` 队列）和 HLS 转码任务（`transcode` 队列）。
- Go API 与独立的 asynq Worker 分进程运行；Worker 从对象存储取音频并调用本机 `whisper-server` 转写，同一进程内另一个 `asynq.Server` 调用宿主机 FFmpeg 生成多码率 HLS（P2-4，见 [ADR 0007](../docs/decisions/0007-hls-transcode-signed-playlist.md)）。
- 客户端音频通过预签名 PUT URL 直传 SeaweedFS。

## 运行依赖

先在仓库根目录的私有进程环境中提供配置，或创建权限为 `0600`、已被 `.gitignore` 排除的 `.env`。不要将凭据提交至 Git。必需的变量为 `POSTGRES_PASSWORD`、`DATABASE_URL`、`REDIS_ADDR`、`S3_ENDPOINT`、`S3_ACCESS_KEY`、`S3_SECRET_KEY`、`API_BEARER_TOKEN`、`API_ADDR`；Worker 还可指定 `ASR_BASE_URL`。`DATABASE_URL` 的数据库密码须与 `POSTGRES_PASSWORD` 一致。`S3_ENDPOINT` 在模拟器阶段用本机回环地址，并与客户端预期的 `JK_S3_ORIGIN` 一致；新增局域网配置前必须重新评估认证与端口暴露。

```sh
make prepare-local-config  # 从私有环境生成忽略的 server/seaweedfs/s3.json
make up-local             # 启动本机依赖；首次启动前检查端口只绑定回环
make migrate-server
make run-whisper          # 独立终端，本机 Whisper
make run-worker           # 独立终端，异步任务
make run-server           # 独立终端，HTTP API
```

`make` 会读取本机 `.env`，但不会将其中的值打印在命令日志；Compose 也会使用该文件。可由操作者在私有环境中注入同名变量。模型不存在时 `make run-whisper` 的下载脚本会访问模型站点，应先确认网络和费用边界。

## 接口

| 方法 | 路径 | 语义 |
| --- | --- | --- |
| POST | `/v1/recordings` | 创建记录并返回预签名 PUT URL（支持背压 `503` 与限流 `429`） |
| POST | `/v1/recordings/{id}/complete` | 确认对象存在，原子写入 outbox 并投递任务（Redis 故障时优雅降级 `200 OK`） |
| GET | `/v1/recordings/{id}` | 查询录音状态、文本和错误 |
| GET | `/v1/recordings/{id}/playback` | 返回原始文件预签名 GET URL `playback_url`，以及 `transcode_status`（`none` / `pending` / `processing` / `ready` / `failed`）；`ready` 时附带签名相对地址 `hls_url` 和 `hls_variants_kbps`，`failed` 时附带 `transcode_error` |
| GET | `/v1/hls/{id}/master.m3u8`、`/v1/hls/{id}/{N}k/index.m3u8` | 免 Bearer，校验 `exp` + `sig`（15 分钟有效）；返回改写后的 playlist：子 playlist 带签名，init 段与分片为 S3 预签名地址；签名无效 `403`，路径非法 `400`，未就绪 `404` |
| GET | `/healthz` | L7 探针（PostgreSQL 查询 + Redis `PING`），免鉴权，正常返回 `200`，异常返回 `503` |
| GET | `/metrics` | Prometheus 文本指标端点（HTTP 直方图、慢查询计数、连接池水位、Outbox 积压、限流/背压计数） |

业务接口（`/v1/*`）要求：

```text
Authorization: Bearer <API_BEARER_TOKEN>
```

所有请求自动解析并透传 W3C `traceparent` 与 `X-Request-ID`。支持通过环境变量配置连接池、慢查询阈值、限流与背压：

- `DB_MAX_CONNS`（默认 `25`）、`DB_MIN_CONNS`（默认 `2`）、`DB_ACQUIRE_TIMEOUT_MS`（默认 `3000`）
- `SLOW_QUERY_THRESHOLD_MS`（默认 `50`）
- `RATE_LIMIT_RPS`（默认 `2000`）、`RATE_LIMIT_BURST`（默认 `2000`）
- `MAX_OUTBOX_BACKLOG`（默认 `50000`）、`OUTBOX_BATCH_SIZE`（默认 `64`）
- Worker 转码：`FFMPEG_PATH`（默认 `ffmpeg`）、`TRANSCODE_CONCURRENCY`（默认 `1`）、`TRANSCODE_NICE`（默认 `10`）、`TRANSCODE_BITRATES_KBPS`（默认 `32,64,96`，须严格递增）

迁移 `0003_add_transcode_jobs.sql` 新增 `transcode_jobs` 与 `transcode_outbox`。上传确认事务同时写入转写 outbox、转码任务和转码 outbox；历史录音没有转码任务，回放接口返回 `transcode_status=none`。

## 验证与压测（P0 & P1）

```sh
make migrate-server
make test-server
make smoke-server
make load            # Go 压测工具（对本地 API 发起 200 并发压测）
make p1-experiments  # P1 全套 6 项实验（并发梯度、连接池拐点、100万行 SQL、故障注入、10万积压背压）
```

`smoke-server` 执行创建、直接上传、确认和查询。它不执行真实 ASR；`make run-whisper` 和 `make run-worker` 启动后，运行 `make smoke-asr` 可验证 `.m4a` 最终进入 `done` 并持久化文本；`AUDIO_FORMAT=ogg make smoke-asr` 改为上传由 core 编码的 Ogg Opus。

`make smoke-hls` 上传 core 编码的 Ogg Opus，按状态变化打印转码与转写进度，校验未签名访问返回 `403`、签名 master 含 3 档、每档为 AAC 且实测码率接近目标；超时或失败时输出 `transcode_jobs`、`transcode_outbox` 与 Worker 日志。`LOOP_SECONDS=180` 可生成较长录音，用于客户端档位切换验证。转码 RTF 与转写干扰实验：`python3 server/scripts/p2-4-hls-bench.py`（需先加载 `.env`）。

`POST /v1/recordings` 的 `content_type` 只接受精确的 `audio/mp4`（对象键 `audio.m4a`）和 `audio/ogg`（对象键 `audio.ogg`），其他值返回 `400`。预签名 PUT 会把 Content-Type 纳入签名，上传时必须使用同一个值。见 [ADR 0006](../docs/decisions/0006-opus-ogg-container.md)。

## Apple 客户端联调（仅可信开发网络）

录音页保存成功后才显示“上传并转写”。点击后按创建、预签名 PUT、确认和状态轮询显示文本；默认没有注入配置时只显示“转写未配置”，不会自动上传。转写期间仍可本地回放，失败保留本地文件供手动重试。

1. 当前先只验证模拟器，`docker-compose.yml` 把 PostgreSQL 5432、Redis 6379、S3 9000 和 SeaweedFS 管理端口 9001/9333 均绑定到 `127.0.0.1`；以 `docker compose ps` 核对实际端口。旧容器不会自动应用新端口映射，须经操作者确认后用 `make up-local` 重建（保留持久化卷）。在单独完成凭据轮换及防火墙检查前，不要把当前静态本地凭据和明文 API 暴露给局域网。
2. 模拟器联调时，使本机 `S3_ENDPOINT` 与客户端注入的 `JK_S3_ORIGIN` 指向同一回环地址；API 在本机监听，Worker 的 `ASR_BASE_URL` 指向本机 Whisper。真实 iPhone 联调需另外确定 Mac/手机都可达且可信的 S3 地址、轮换私有凭据和核查防火墙；不能在客户端替换预签名 URL 的 Host。
3. 模拟器 App 启动进程注入 `JK_API_BASE_URL`、`JK_S3_ORIGIN` 和 `JK_API_BEARER_TOKEN`，三者使用与本机 API/S3 相同的地址和凭据。可以在私有终端使用 `SIMCTL_CHILD_JK_*` 配合 `xcrun simctl launch <模拟器 UUID> com.jkmeeting.ios`；**不要将令牌字面量写进终端历史、Scheme、截图或仓库文件**。从 Xcode 重新 Run 或从桌面重新启动不会继承先前注入值，需重新注入；缺项会显示“转写未配置”，而不是使用默认 token。
4. 先使用 `make test-apple-macos`、`IOS_SIMULATOR_ID=<模拟器 UUID> make test-apple-ios` 验证共享测试，再在 Mac 与 iPhone 真机各完成一段 30 秒录音、本地回放、手动上传、状态到 `done` 并显示转写文本；断网、时限和中断验收及四项基线单独记录到 `docs/metrics.md`。未亲自完成设备验证不能视为 P0 完成。

P0 使用固定 Bearer 和明文 HTTP，无多用户隔离，仅在可信开发网络联调，不可面向公网或生产复用。Debug Apple App 才允许开发用 HTTP，Release 不放宽 ATS。
