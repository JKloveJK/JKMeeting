# P0 Apple 客户端转写闭环实施方案

- 日期：2026-09-30
- 需求：`docs/requirements/2026-09-30-p0-apple-asr-integration-requirements.md`
- 阶段：独立评审已通过，实施中；结论见 `docs/reviews/2026-09-30-p0-apple-asr-integration-review.md`。

## 架构与数据流

保留现有双端本地录音/播放。录音完成后手动点击“上传并转写”，由 `apple/Shared/Recording/RecordingViewModel.swift` 驱动独立转写状态，调用新增的共享可注入网络客户端：POST `/v1/recordings`（duration_ms、size_bytes、audio/mp4）→直接向预签名 URL PUT `.m4a` 文件→POST `/complete`→有界轮询 GET 直到 done/failed。成功显示文本；失败保留本地文件和重试入口。API 请求带 Bearer，预签名 PUT 不带。新录音/离开页面取消任务并校验录音代次，旧结果不得覆盖当前录音。录制后的播放优先用本地文件，GET `/playback` 用于需要的远端文件验证，不自动重复下载。

## 安全、错误与平台配置

运行时使用 `ProcessInfo.processInfo.environment` 读取 `JK_API_BASE_URL`、`JK_S3_ORIGIN`、`JK_API_BEARER_TOKEN`；缺项或无效时 App 显示未配置，绝不使用硬编码回退。Simulator 可由 `SIMCTL_CHILD_JK_*` 环境经 `xcrun simctl launch` 注入；真机由 `DEVICECTL_CHILD_JK_*` 环境经 `xcrun devicectl device process launch --device <ID> <bundle-id>` 注入（两者的 `--help` 已核实支持前缀），macOS 从启动进程环境注入。只在当前进程生命周期有效，系统自行重启时会显示未配置；避免在命令行字面写入 token，交互环境变量从调用者私有环境提供并关闭命令回显，不记录到 shell 历史、plist、Scheme、项目脚本或日志；首次在真机启动后核对未注入时的未配置提示与注入后的已配置提示，不显示实际 token。

实施前先调整 `docker-compose.yml`，将 5432/6379/9001/9333 明确绑定 `127.0.0.1`，仅 9000 保留面向受信任局域网监听；确认 Docker/Colima 的实际监听、防火墙与机器 IP，并确保管理端口未暴露。为保留原本本机默认工作流，Mac 服务启动仍由显式运行时 `API_ADDR=<Mac局域网IP>:8082`、`S3_ENDPOINT=http://<Mac局域网IP>:9000` 覆盖现有本机默认值，API 和 Worker 使用同一 S3 地址，客户端 `JK_API_BASE_URL` 和 `JK_S3_ORIGIN` 使用对应地址；启动前由操作者检查 iPhone 网络可达、端口暴露情况和仅受信任 Wi-Fi，禁止 0.0.0.0 API 盲开公网。预签名 Host 不能由客户端改写，API 和 Worker 也必须能用此地址访问 S3。

只接受绝对 URL、scheme 为 `http`（P0 仅局域网开发）或 `https`、无 userinfo/fragment、host/有效端口精确等于预期 `JK_S3_ORIGIN`，路径为对象存储录音桶路径、非空签名 query；禁止协议降级、非白名单重定向与外部 host 的 PUT/GET。URLSession 的重定向委托一律拒绝预签名请求的 3xx，返回通用错误；所有网络错误消息不得携带包含 query 的 `URL`、Bearer、完整响应体或转写内容。PUT 直接使用服务端返回的 URL 并上传原始文件字节，仅 `Content-Type: audio/mp4`，绝不带 Bearer；API 请求仅在白名单 API 基址添加 Bearer。为此新增负向测试覆盖 userinfo、fragment、端口/host 偏差、无签名、重定向及错误脱敏。macOS 增加网络 client entitlement；两端只为 Debug 开发构建增加本地 HTTP ATS 例外，iOS 添加本地网络用途说明；Release 不应使用明文例外。固定 token 本质为无用户隔离的明文 P0 开发凭据，仅可信局域网使用，不进入公网/生产。

请求失败按阶段处理：非 2xx、超时、取消、非法 JSON 明确提示。创建成功后保存 recording_id、有效期与阶段：PUT 结果未知时仍可在有效期内对原 URL 重传原文件（覆盖同一对象），最多两次；过期/签名失效则重新创建记录并上传，旧 created 记录保留供后续清理，不能依赖重新签同一 ID（服务端无接口）。complete 因 409/网络错误/500 结果不明时先 GET：uploaded/transcribing/done 表示继续轮询；created 可能是对象未上传，先按原有效 PUT 地址补传再 complete，仍不成功则建立新录音记录并上传；failed 是 ASR 终态（服务端没有重转写接口），用户手动重试必须新建记录重新上传。所有恢复都有最多两次请求重试及截止时间，取消立即停止；轮询每 2 秒检查状态，至多覆盖 120 秒 ASR 和排队预算，超时提示并保留 ID 供手动继续查询。不能假设 complete 天然幂等。

## 录音剩余边界

`apple/Shared/Recording/AppleRecordingManager.swift` 与 `AppleAudioCapture.swift` 接入前台 5 分钟自动停止及音频会话中断。停止先禁止新 tap 帧进入，再由投递串行队列 drain 已成功接收的帧并等待 pipeline 队列同步屏障，之后关闭 writer→回读校验；不能先将 generation 置为停止再跳过已入队尾帧。单一完成门闩只允许一次用户/超时/中断收尾及一次 completion，异步事件在 main actor 序列化。处理队列总待处理帧数必须有可观察上界，丢帧达到阈值时给用户明确失败，不能无限排队。`RecordingViewModel.swift` 保留 `.durationLimit`/`.interrupted` 原因，拒绝权限时显示设置引导。测试含极短录音最后一帧、重复 stop、限时与中断同时到达、空文件不得进入 recorded、队列满时失败；不把平台音频 API 放进 `core/`。

## 文件与实施步骤

1. 修改 `apple/Shared/Recording/AppleAudioCapture.swift`、`AppleRecordingManager.swift`、`RecordingViewModel.swift` 及 `apple/SharedTests/`：先写失败测试，再接入限时/中断/安全收尾。
2. 新增 `apple/Shared/Recording/RecordingAPIClient.swift` 及共享测试，用可注入 URLSession/HTTP transport 验证协议、直传文件、状态轮询、白名单、重试与取消；避免新增第三方依赖。
3. 修改 `RecordingViewModel.swift`、`apple/App/Features/Recording/RecordingView.swift` 与共享状态测试：接入手动转写按钮、状态/文本/错误/重试，保留播放。
4. 修改 Apple 双端 `Info*.plist`、`JKMeeting-macOS.entitlements` 及必要的 `apple/project.yml`，由开发运行环境注入配置；不改生成工程作为唯一数据源。
5. `xcodegen generate --spec apple/project.yml --project apple` 后运行 `xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'platform=iOS Simulator,id=<本机UUID>'` 与 `xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'`，执行 `make test-core`、`make test-apple-bridge`、`make test-server` 与依赖就绪时 `make smoke-asr`；保留原始日志/xcresult。更新 P0 设计和 `docs/metrics.md` 的实际进度。Mac/iPhone 真实设备各录制 30 秒→本地回放→手动上传→`done` 文本，注入断网检验失败/重试，再检验 5 分钟与中断。基线各记录环境、音频时长、文件大小、时间戳、样本量与日志原始路径：上传 P95 用多次相同大小 PUT 样本计算；ASR RTF 用音频时长除以转写耗时，标注与目标判据的定义是否一致；停止→UI 文本测总时长；core 累计处理耗时/采集时长通过录音层计时汇总。未测/只单次样本不能称 P95，仍标待验收。

## 评审修订说明

针对首次独立评审五项阻塞修订：① 使用 `simctl`/`devicectl` 进程级环境注入并校验无配置反馈；② 明确 Compose 非业务端口 loopback 与 API/S3 局域网可达的启动检查；③ 固定签名 URL 检查、禁止重定向及敏感错误脱敏；④ 按 PUT/complete/ASR 不同失败阶段定义有界重试/新建策略；⑤ 定义先 drain 后关闭的尾帧屏障、并发停止门闩与双端 XCTest/指标原始证据。待同一独立评审员仅核对这五项并给出通过/不通过结论。
