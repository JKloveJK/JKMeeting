# P0 Apple ASR 闭环方案独立评审

- 日期：2026-09-30
- 对象：`docs/requirements/2026-09-30-p0-apple-asr-integration-requirements.md`、`docs/plans/2026-09-30-p0-apple-asr-integration-implementation.md`
- 评审结论：**通过**（仅代表方案修订通过，不代表代码或真机验收完成）。

## 首次评审阻塞与修订核对

| 首次评审问题 | 修订与复核结论 |
| --- | --- |
| iPhone 运行时注入配置缺少可执行路径 | 方案明确 `simctl`/`devicectl` 进程级环境注入、无配置反馈和不持久化 token；通过。 |
| 局域网开放与 Compose/Makefile 端口现状冲突 | 方案明确非业务端口 loopback、S3 受信任局域网可达、API/S3/Worker 同步配置及验证顺序；通过。 |
| 预签名 URL 规则不精确 | 方案定义白名单、禁止 userinfo/fragment/越界重定向、Bearer 隔离、错误脱敏及负向测试；通过。 |
| PUT/complete/ASR failed 重试分支不完整 | 方案分别定义状态对账、有限重试、过期/失败时重建录音；通过。 |
| 停止时尾帧、重复停止和验收证据不充分 | 方案定义队列排空屏障、完成门闩、双端 XCTest 入口和四项基线原始证据；通过。 |

## 边界

同一评审员复核仅针对首次五项问题；不参与方案编写或编码。实施时必须以测试与设备行为验证各方案假设，未验证的结果不得标为 P0 完成。
