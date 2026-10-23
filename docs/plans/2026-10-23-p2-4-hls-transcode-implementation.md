# P2-4 服务端 HLS 转码与多码率回放技术方案

- 日期：2026-10-23
- 状态：已实施，待用户终审
- 需求：[P2 音频管道需求 · P2-4](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)
- 决策：用户于 2026-10-23 授权“按推荐方案直接实施”，即 AAC-LC fMP4 三档、独立 `transcode_jobs` 表与 `transcode_outbox`、本机 FFmpeg。

## 1. 目标

上传确认后异步生成多码率 HLS（32 / 64 / 96 kbps AAC-LC，fMP4 分片 6 秒），与转写互不阻塞；Apple 端通过 `AVPlayer` 播放并可切换码率。

## 2. 关键取舍

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| 编码与容器 | AAC-LC、fMP4、`-ar 48000 -ac 1` | `AVPlayer` 对 HLS AAC 支持最成熟；本机 FFmpeg 8.1 自带 `aac` 编码器（已验证产物结构，见 §4） |
| 状态存储 | 新表 `transcode_jobs`（以 `recording_id` 为主键）+ `transcode_outbox` | 不改动 `recordings` 和转写 outbox 的唯一约束；转码失败不影响转写状态 |
| 投递 | 与转写相同的事务 outbox：上传确认事务内同时写两个 outbox | 复用 P1 已验证的“至少一次投递 + 幂等 task_id”模式 |
| 并发与优先级 | 转写与转码分属两个 asynq 队列（`default` / `transcode`），在同一 Worker 进程内由两个 `asynq.Server` 消费；转码并发上限 `TRANSCODE_CONCURRENCY`（默认 1）；FFmpeg 以 `nice -n TRANSCODE_NICE`（默认 10）运行 | asynq 没有按队列的并发上限，权重只影响出队概率，不能防止 FFmpeg 占满 CPU；独立 Server 才能给转码单独设上限，`nice` 让 CPU 竞争时 Whisper 优先 |
| HLS 访问鉴权 | API 提供 `GET /v1/hls/{id}/{variant}/index.m3u8` 与 `master.m3u8`，用 URL 签名（`exp` + HMAC-SHA256）代替 Bearer；返回时改写 playlist：子 playlist 指向带签名的 API 地址，分片和 init 段替换为 S3 预签名 GET 地址 | `AVPlayer` 不便附加 `Authorization` 头；分片直连 S3，API 只处理很小的 playlist |
| 签名密钥 | 由 `API_BEARER_TOKEN` 经 HMAC 派生（标签 `jkmeeting-hls-v1`） | 不新增密钥配置；有效期 15 分钟，与现有预签名一致 |

## 3. 数据模型（迁移 `0003_add_transcode_jobs.sql`）

- `transcode_jobs`：`recording_id`（主键，外键级联删除）、`status`（`pending` / `processing` / `ready` / `failed`）、`attempts`、`lease_token`、`lease_until`、`master_key`、`variants`（如 `32,64,96`）、`transcode_ms`、`error_message`、时间戳。
- `transcode_outbox`：列与 `transcription_outbox` 相同。
- 历史录音没有 `transcode_jobs` 行，回放接口按 `none` 处理，不自动补转码。

## 4. 数据流

1. `POST /v1/recordings/{id}/complete`：同一事务内将录音置为 `uploaded`，插入 `transcription_outbox`、`transcode_jobs(pending)`、`transcode_outbox`；随后两个 dispatcher 同步派发（失败时由后台循环自愈）。
2. 转码 Worker：领取租约（`pending` 或租约过期的 `processing`）→ 下载原始对象 → 在临时目录运行 FFmpeg → 上传 `recordings/<id>/hls/` 下所有文件 → 标记 `ready`，写入 `master_key`、`variants`、`transcode_ms`。
3. 回放：`GET /v1/recordings/{id}/playback` 增加 `transcode_status`；状态为 `ready` 时返回相对地址 `hls_url`（带签名），客户端基于 API 地址解析。

FFmpeg 命令（已在本机验证，14 秒样本生成 3 档 × 3 个分片和各档 init 段）：

```bash
nice -n 10 ffmpeg -hide_banner -nostdin -v error -y -i in.audio \
  -map 0:a -map 0:a -map 0:a -c:a aac -ac 1 -ar 48000 \
  -b:a:0 32k -b:a:1 64k -b:a:2 96k \
  -f hls -hls_time 6 -hls_playlist_type vod -hls_segment_type fmp4 \
  -hls_fmp4_init_filename init.mp4 -hls_segment_filename '%v/seg_%03d.m4s' \
  -master_pl_name master.m3u8 -var_stream_map 'a:0,name:32k a:1,name:64k a:2,name:96k' '%v/index.m3u8'
```

## 5. 错误处理

| 场景 | 处理 |
| --- | --- |
| 找不到 FFmpeg 可执行文件 | 直接标记 `failed`（配置错误，重试无效），返回 `asynq.SkipRetry` |
| FFmpeg 非零退出（输入损坏或不支持） | 标记 `failed`，错误信息截取 stderr 末尾 200 字符，`SkipRetry` |
| 下载 / 上传 / 数据库临时错误 | 释放租约回到 `pending`，返回错误由 asynq 重试；第 3 次仍失败则标记 `failed` |
| 签名过期或错误 | `403`；路径不符合白名单（防目录穿越）为 `400` |

## 6. Apple 端

- `RecordingAPIClient` 在转写完成后保留 `recording_id`，新增 `playbackInfo(recordingID:)`。
- `HLSPlaybackController`：`AVURLAsset.load(.variants)` 读取档位，`AVPlayerItem.preferredPeakBitRate` 切换，并通过 access log 的 `indicatedBitrate` 确认切换生效与耗时。
- 界面：转写完成且 HLS 就绪后显示“远程回放”与档位选择。

## 7. 测试策略

| 层 | 测试 |
| --- | --- |
| `queue` | 转码任务类型、队列与载荷编解码；dispatcher 使用任务工厂投递到对应队列 |
| `transcode` | FFmpeg 参数构造；handler 在成功、FFmpeg 缺失、FFmpeg 失败、下载失败（重试与第 3 次失败）下的状态转换与上传键（fake 依赖） |
| `httpapi` | 回放接口返回 `transcode_status` / `hls_url`；签名 playlist 改写；过期签名 `403`；非法路径 `400` |
| 集成 | `make smoke-hls`：上传 → `ready` → 校验 master 档位 → 每档 `ffprobe`；同时确认转写为 `done` |
| 压测 | `server/scripts/p2-4-hls-bench.py`：转码 RTF；对比 0～3 个并行 FFmpeg 下的 Whisper 推理 P95（见 §9） |
| Apple | `HLSPlaybackControllerTests`：设置 `TEST_RUNNER_JK_HLS_URL` 时连接本地栈验证档位读取与切换，未设置时跳过 |

## 8. 不做

视频、DRM、直播 HLS、CDN、历史录音自动补转码、按用户的码率自适应策略。

## 9. 实施修正

| 项 | 原计划 | 实际 | 原因 |
| --- | --- | --- | --- |
| Apple 接口名 | `playbackInfo(recordingID:)` | `RemotePlaybackProviding.remotePlayback(recordingID:)`，`RecordingAPIClient` 记录 `lastRecordingID` | 以协议注入 ViewModel，便于用 fake 测试轮询逻辑 |
| 转写干扰压测 | 同时提交 N 个录音，经 Worker 对比转写 P95 | 直接请求 whisper-server `/inference`，并行运行 0～3 个与 `FFmpeg.Args` 相同参数的 FFmpeg | 长录音自身的转写任务会排在串行转写队列前面，把排队时间混进推理延迟 |
| HLS 限流 | 未说明 | HLS 路由在签名校验前同样走全局令牌桶 | 免 Bearer 的路由不应绕过限流 |
| 档位切换验证输入 | 冒烟样本 | `LOOP_SECONDS=180` 的长录音 | 短样本起播后全部缓冲，切换不会生效（见问题排查记录） |
| `ffprobe` 码率断言 | 读取 ffprobe 码率 | 按分片实际字节计算 | ffprobe 对 playlist 输出的 `format.bit_rate` 不反映媒体码率（三档均为 691） |
