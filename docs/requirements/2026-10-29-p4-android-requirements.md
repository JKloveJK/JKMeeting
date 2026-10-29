# P4 Android 端接入需求

- 日期：2026-10-29
- 状态：已实施（模拟器口径，见 [P4 报告](../reports/2026-10-29-p4-android-report.md)）；真机与真人录音验证为已知缺口
- 路线：[客户端全栈学习路线 · P4](../客户端全栈学习路线.md)
- 用户已确认（2026-10-29）：模拟器验收；分阶段连续推进，不等待单阶段终审。

## 背景与目标

用第二个平台检验 C++ core 的抽象是否成立：Kotlin + Compose 只画 UI，核心逻辑经 JNI 调同一份 `core/`，**core 代码改动量就是抽象质量的度量**。

## 范围

1. `android/` Gradle（Kotlin DSL）工程：Compose UI、ViewModel + StateFlow 单向数据流。
2. JNI 桥接（`externalNativeBuild` + CMake）：把 `core/` 的 `AudioSession`（48 kHz → 16 kHz 重采样、增益、静音检测）以静态库形式链接，桥接层只做类型转换，不复制业务逻辑。
3. `AudioRecord` 采集 → 按 1024 样本分帧 → JNI 处理 → 写 16 kHz 单声道 WAV → `MediaPlayer` 回放；处理耗时与统计量（丢帧填充、语音/静音时长）在 UI 展示。
4. 纯 Kotlin 逻辑（WAV 读写、分帧）写 JVM 单测；JNI 行为写 instrumented 测试，在模拟器运行。

## 非目标

- 不做上传/转写网络链路（服务端接口已由 Apple 端验证；Android 网络层待主链路稳定后单独立项）。
- 不做后台录音、权限之外的系统集成、真机适配。

## 验收标准

| 编号 | 标准 | 证据 |
| --- | --- | --- |
| A1 | `core/` 为支持 Android 的改动量 = 0（或极小且逐项说明） | `git diff` 统计 |
| A2 | 模拟器上完成录音 → core 处理 → 回放主链路 | instrumented 测试 + 模拟器运行记录 |
| A3 | JNI 输出与桌面 core 单测口径一致（30 秒输入输出时长误差 ≤ 1 帧） | instrumented 测试 |
| A4 | 冷启动 < 800 ms（模拟器，`am start -W` 口径，注明与真机差异） | 命令输出记录于报告 |
| A5 | JVM 单测通过、APK 构建成功 | `make test-android` |
