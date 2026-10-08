# JKMeeting 指标台账

> 指标用于验证学习结论与优化收益，不用于“看起来很快”。每次记录必须包含环境、输入、命令、原始结果路径和结论；没有基线不能宣称优化。

## 1. 记录规则

- 每个指标记录 P50 / P95 / P99（适用时）、样本量、错误率与测量日期。
- 性能比较必须保持同一输入、同一版本、同一环境；条件变化时新开一行，不覆盖历史值。
- 原始日志、压测输出、trace 或 SQL 计划保留在对应实验报告中，路径填入“证据”列。
- 结论只描述数据实际支持的范围；若存在混杂因素，标为“待复测”。

## 2. P0 基线（端到端闭环）

| 指标 | 定义 | 目标/判据 | 当前值 | 环境与输入 | 命令/证据 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 上传 P95 | 录音文件 PUT 预签名 URL 的 P95 耗时 | 首次建立基线；1.2 MB、局域网场景 | 未测量 | 待填写 | 待填写 | 未开始 |
| ASR RTF | 音频时长 / whisper 转写耗时 | `RTF < 1` | 未测量 | whisper.cpp base，待填写 | 待填写 | 未开始 |
| 松手到文本 | 用户停止录音到界面显示完整文本的总时长 | `< 15s`（30 秒音频、whisper base） | 未测量 | 待填写 | 待填写 | 未开始 |
| core 耗时占比 | C++ core 处理耗时 / 音频采集时长 | 为 P2 实时化决策提供基线 | 未测量 | 16 kHz、单声道，待填写 | 待填写 | 未开始 |
| 服务端基础链路 | 创建 → S3 PUT → 确认 → 查询 → ASR | 真实 `.m4a` 进入 `done` 并持久化 transcript | 通过 | macOS、Colima、PostgreSQL 16、Redis 7、SeaweedFS 3.68、whisper.cpp base；`say` 生成 AAC/m4a | `make migrate-server && make test-server && make smoke-server && make smoke-asr` | 已验证；性能基线待单独测量 |
| Apple 录音模块 | 双端状态机、采集壳层、m4a 回读、播放控制 | macOS/iOS Simulator 单测、双端构建、Simulator 启动 | 通过 | Xcode 27、iPhone 17 Pro Simulator（iOS 26.2） | macOS/iOS `xcodebuild test`；双端 `xcodebuild build`；`output/ios-recording-interface-stable.png` | 2026-10-04 用户补充确认模拟器 6 秒录制及本地回放正常；Mac/iPhone 物理设备仍待测 |
| 本机合成语音转写复验 | 创建 → 本机 S3 PUT → complete → Worker → Whisper → `done` → 预签名 GET | 单次模拟服务链路成功，不等同 App 录音或性能基线 | 通过（1 次合成音频） | 2026-10-04，Mac/Colima、loopback，`say` 合成约 2.8 秒语音，AAC/m4a | `server/scripts/smoke-asr.sh`：`output/p0-local-asr-smoke.log`；API/Worker/ASR：`output/p0-runtime-{api,worker,whisper}.log`；DB 状态 `done` 且文本已持久化，预签名 GET 返回 200/audio/mp4 | 模拟器 App 已注入运行时配置并重启；用户录音→上传→屏幕出文本仍待操作验证 |

## 3. 后续阶段预登记

| 阶段 | 指标 | 触发条件 | 当前状态 |
| --- | --- | --- | --- |
| P1 | API P50/P95/P99、吞吐、错误率、DB 连接池与资源使用 | 开始压测或调优 | 未开始 |
| P1 | 故障恢复时间、队列积压、慢查询计划 | 开始故障注入或大数据量实验 | 未开始 |
| P2 | 编解码 RTF、丢帧率、回放首帧、多码率切换 | 引入 Opus / 转码 / 回放优化 | 未开始 |
| P3 | 结构化输出通过率、任务准确率、端到端 P95、单次成本 | 接入 LLM 或 RAG | 未开始 |
| P4 | iOS/Android 启动、采集、播放对比；core 改动量 | Android 接入 | 未开始 |
