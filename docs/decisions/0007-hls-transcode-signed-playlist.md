# 0007：服务端异步转码多码率 HLS，并以签名 playlist 代理提供回放

- 日期：2026-10-23
- 状态：已接受
- 关联：[P2 需求 · P2-4](../requirements/2026-10-22-p2-audio-pipeline-requirements.md)、[P2-4 方案](../plans/2026-10-23-p2-4-hls-transcode-implementation.md)、[实验报告](../reports/2026-10-23-p2-4-hls-report.md)、[ADR 0006](0006-opus-ogg-container.md)

## 背景

P2-2 之后录音以 Ogg Opus 24 kbps 上传。路线文档要求 P2-4 在服务端生成多码率 HLS，让客户端按网络切换档位，并且转码不能拖慢转写。约束有三条：

- 转码必须与转写解耦，转码失败不能影响转写；
- `AVPlayer` 拉取 playlist 和分片时不便附加 `Authorization` 头，而 S3 对象不能公开；
- docker-compose 不新增服务，FFmpeg 使用宿主机安装的版本（`FFMPEG_PATH`）。

## 决定

- **格式**：AAC-LC 单声道 48 kHz，三档 32 / 64 / 96 kbps（`TRANSCODE_BITRATES_KBPS`），fMP4 分片，每片 6 秒，VOD playlist。产物保存在 `recordings/<id>/hls/` 下：`master.m3u8`、`<N>k/index.m3u8`、`<N>k/init_<i>.mp4`、`<N>k/seg_NNN.m4s`。
- **状态与投递**：
  - 新表 `transcode_jobs`（以 `recording_id` 为主键）与 `transcode_outbox`，迁移文件为 `0003_add_transcode_jobs.sql`；
  - 上传确认事务内同时写入转写 outbox、`transcode_jobs(pending)` 和转码 outbox，复用 P1 的事务 outbox 与租约模式；
  - `recordings` 表和转写状态机不改。
- **隔离**：
  - 转码使用 asynq 的 `transcode` 队列，由同一 Worker 进程内另一个 `asynq.Server` 消费，并发上限为 `TRANSCODE_CONCURRENCY`（默认 1）；
  - FFmpeg 以 `nice -n TRANSCODE_NICE`（默认 10）运行。
- **失败语义**：
  - 找不到 FFmpeg 或 FFmpeg 非零退出属于确定性错误，直接标记 `failed`，不重试；
  - 下载、上传、数据库等临时错误释放租约后重试，第 3 次仍失败才标记 `failed`。
- **回放鉴权**：
  - `GET /v1/hls/{id}/master.m3u8` 与 `GET /v1/hls/{id}/{N}k/index.m3u8` 不要求 Bearer，改为校验 URL 签名：`exp` 加上 HMAC-SHA256(`id|exp`)；签名密钥由 `API_BEARER_TOKEN` 经 HMAC 派生（标签 `jkmeeting-hls-v1`），有效期 15 分钟；
  - API 返回 playlist 前会改写：子 playlist 地址带上同一签名，init 段和分片替换为 S3 预签名 GET 地址；
  - API 只返回很小的 playlist，音频字节直接从 S3 下载；
  - 路径白名单为 `^(master|[0-9]{1,3}k/index)\.m3u8$`，playlist 中出现绝对地址或会跳出 HLS 目录的相对路径时拒绝；
  - HLS 路由同样受全局限流约束。
- **客户端**：
  - `GET /v1/recordings/{id}/playback` 新增 `transcode_status`（`none` / `pending` / `processing` / `ready` / `failed`），就绪时再返回带签名的相对地址 `hls_url` 和 `hls_variants_kbps`；
  - Apple 端只接受同一 API 源下、属于该录音的 master 地址，用 `preferredPeakBitRate` 切换档位。

不选择：

- **asynq 队列权重**：权重只决定出队概率，无法限制转码同时占用多少 worker 或 CPU。
- **在 `recordings` 上加转码列**：会把两条独立状态机耦合在一起，转码失败的语义也会污染转写状态。
- **公开桶或长期有效的预签名 playlist**：playlist 内的分片地址需要在每次请求时重新签发；公开桶违反隐私边界。
- **Opus fMP4 或 CMAF 多编码**：`AVPlayer` 对 AAC HLS 支持最成熟；本阶段只验证码率阶梯，不做编码对比。
- **docker-compose 新增 FFmpeg 容器**：用户确认使用宿主机 FFmpeg；容器化留到部署阶段。

## 依据

- `go test ./...`：覆盖 transcode handler 的成功、FFmpeg 缺失、FFmpeg 失败、临时错误重试与第 3 次失败；签名的过期、篡改、换录音、缺失；路径穿越；playlist 改写。
- `make smoke-hls`：23.5 秒样本生成三档，每档 4 个分片；按分片实际字节计算的码率为 33 / 66 / 100 kbps；未签名请求返回 403；转写同时完成。
- `server/scripts/p2-4-hls-bench.py`：转码 RTF 中位数为 0.0115（600 秒）和 0.0137（60 秒）。有无 FFmpeg 负载时 Whisper 推理 P95 没有可测的劣化，原因是 whisper.cpp 在 M1 Pro 上走 Metal GPU，与 CPU 上的 FFmpeg 基本不争用（详见实验报告）。

## 后果

- 每条新录音多出一次转码，以及约“时长 × (32+64+96) kbps”的存储，约为原始 24 kbps Opus 的 8 倍。
- 历史录音没有 `transcode_jobs` 行，回放接口返回 `none`，不自动补转码。
- HLS 签名密钥随 `API_BEARER_TOKEN` 轮换；轮换后已签发的 playlist 地址立即失效。
- Worker 所在机器必须安装 FFmpeg。缺失时转码任务标记为 `failed`，转写不受影响。
- 当前机器上 `nice` 和并发上限是预防性措施，没有测出收益；如果改用纯 CPU 运行 Whisper，需要重新测量。
