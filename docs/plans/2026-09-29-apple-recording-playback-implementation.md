# Apple 双端录音与回放实施计划

- 日期：2026-09-29
- 对应需求：`docs/requirements/2026-09-29-apple-recording-playback-requirements.md`
- 前置条件：P0 步骤 1 至 4 已完成；Apple 工程、`CoreProcessor`、Go 服务与异步 ASR 均已验证。
- 状态：评审阻塞项已获用户授权直接修订并实施

## 设计边界

`apple/project.yml` 继续是唯一工程来源。iOS/macOS 各自的 Shared framework 直接编译 ObjC++ bridge 与 C++ core；`App` 仅组合界面与平台壳层；两个 XCTest bundle 各自导入相应 Shared framework。采集、权限、文件和播放留在 Swift / AVFoundation 壳层，C++ core 只接收同步、已校验的 PCM。

录音采用 `AVAudioEngine` input node tap。tap 内不得进行网络、磁盘阻塞或 UI 更新；它只复制必要的 PCM 到受控写入队列，由该队列串行调用 `CoreProcessor` 并写入原生音频文件。P0 限制前台、单段、≤5 分钟。

## 评审修订说明

| 评审问题 | 修订方式 |
| --- | --- |
| 真机权限与沙箱前置项缺失 | `Info-iOS.plist` 增加 `NSMicrophoneUsageDescription`；macOS target 启用 sandbox，并在 entitlements 中写入 `com.apple.security.device.audio-input=true`。 |
| 中断后状态与文件保留冲突 | 停止引擎并关闭 writer 后，以 `AVAudioFile` 回读校验；有效 m4a 转 `recorded`，无效或空文件删除后转 `failed`。 |
| tap 存在线程与内存风险 | tap 只复制 PCM 至有界队列；队列满时计数丢帧，超过阈值进入 `failed`；单一串行处理队列独占 `CoreProcessor` 和 writer。 |
| PCM 到 m4a 边界未定义 | 处理队列用 `AVAudioConverter` 将 core 的 S16LE 输出转换为 AAC，writer 写入 m4a；stop 后必须可被 `AVAudioFile` 解码回读。 |
| 平台中断与配置变化覆盖不足 | iOS 处理 interruption、route change、media services lost/reset；macOS 处理 engine configuration change。需要重建引擎的事件不得静默继续录音。 |
| 真机验收与指标不完整 | Mac 与 iPhone 真机均验证录制、回读和播放；记录设备、OS、输入格式、时长、字节、队列深度、丢帧、事件与错误到 `docs/metrics.md`。 |
| 验证越出本切片 | 任务 5 不运行 server 或 ASR 冒烟；仅执行 Apple/core 相关验证。 |

> 用户于 2026-09-29 授权按以下最小修订直接实施，不再追加评审：中断或 5 分钟到期时，统一执行“停止采集 → 关闭 writer → `AVAudioFile` 回读校验”；有效 m4a 进入 `recorded` 并显示“录音已停止，可回放”，无效或空文件删除后进入 `failed` 并显示原因。该收尾路径必须由状态机单测覆盖。

## 任务 1：定义共享状态、权限和平台协议

**文件：**
- 新建：`apple/Shared/Recording/RecordingState.swift`
- 新建：`apple/Shared/Recording/AudioCapture.swift`
- 新建：`apple/AppTests-iOS/RecordingStateTests.swift`
- 新建：`apple/AppTests-macOS/RecordingStateTests.swift`

**先失败的测试：**

1. 断言 `idle → requestingPermission → recording → stopping → recorded` 的合法转换。
2. 断言权限拒绝、引擎错误与无效中断文件转为 `failed`；有效中断文件与 5 分钟自动停止转为 `recorded` 并保留可读停止原因。
3. 断言 `recorded → playing → recorded` 合法；非法状态转换被拒绝。

**最小实现：**

- 定义无 AVFoundation 依赖的 `RecordingState`、`RecordingError` 和 `RecordingMetadata`。
- 定义 `AudioCapture`、`AudioPlayback` 协议；协议不泄漏平台实现。
- 录音视图模型只依赖协议，以便使用 fake 验证状态机。

**验证：**

```sh
xcodegen generate --spec apple/project.yml --project apple
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'id=<已安装 iPhone Simulator UUID>'
```

## 任务 2：实现 iOS/macOS 平台权限与采集壳层

**文件：**
- 新建：`apple/App/Audio/IOSAudioCapture.swift`
- 新建：`apple/App/Audio/MacAudioCapture.swift`
- 新建：`apple/App/Resources/JKMeeting-macOS.entitlements`
- 新建：`apple/AppTests-iOS/IOSAudioCaptureTests.swift`
- 新建：`apple/AppTests-macOS/MacAudioCaptureTests.swift`
- 修改：`apple/App/Resources/Info-iOS.plist`
- 修改：`apple/project.yml`

**先失败的测试：**

1. 以注入的权限提供者模拟授权、拒绝和不可用状态，断言平台壳层输出正确领域结果。
2. 以 fake engine 断言启动失败不会写入成功元数据，停止操作会移除 tap。
3. 以确定性 PCM sink 断言平台壳层把采集格式与 frame 交给共享编排层。

**最小实现：**

- iOS `Info.plist` 写入 `NSMicrophoneUsageDescription`；macOS 启用 sandbox 与 `com.apple.security.device.audio-input=true`，再分别请求麦克风权限。
- 两端均使用 `AVAudioEngine` input node 和 tap；tap 只复制 PCM 至有界队列，不做阻塞写盘或 UI 更新。队列满时记录丢帧，达到阈值后显式失败。
- 单一串行处理队列独占 `CoreProcessor` 与 writer；显式协商单声道、16 kHz 处理格式，设备输入不匹配时交由 `CoreProcessor` 重采样。
- 注册 iOS interruption、route change、media services lost/reset，以及 macOS engine configuration change；需要重建引擎时停止当前录音并显式呈现结果。
- 将 AVFoundation 特有代码仅放在 `App/Audio`，通过协议输入 Shared 层。

**验证：**

```sh
xcodegen generate --spec apple/project.yml --project apple
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'id=<已安装 iPhone Simulator UUID>'
```

> Simulator 只验证构建与 fake 测试；真实麦克风必须由 Mac 本机与 iPhone 真机验证。

## 任务 3：实现 core 处理后原生 AAC/m4a 落盘

**文件：**
- 新建：`apple/Shared/Recording/RecordingPipeline.swift`
- 新建：`apple/App/Audio/AppleAudioFileWriter.swift`
- 新建：`apple/AppTests-iOS/RecordingPipelineTests.swift`
- 新建：`apple/AppTests-macOS/RecordingPipelineTests.swift`

**先失败的测试：**

1. 输入确定性 S16LE PCM，断言 pipeline 调用 `CoreProcessor`、保留 timestamp / channel metadata，并向 writer 发送处理结果。
2. writer 返回失败时，断言状态转为 `failed` 而不是 `recorded`。
3. 正常 stop 后断言 metadata 包含非空本地文件 URL、时长和字节数。

**最小实现：**

- `RecordingPipeline` 在串行队列中进行 `CoreProcessor` 调用与写入编排。
- `AppleAudioFileWriter` 使用 `AVAudioConverter` 将 core 的 S16LE 输出转换为 AAC，再以 AVFoundation 写入 m4a；输出路径放在 app sandbox 的 Application Support 或 Documents 子目录。
- 不将 raw PCM 全量累积到内存；按 frame 写入并在 stop 时原子关闭文件。关闭后必须以 `AVAudioFile` 解码回读校验；有效文件才产生 `recorded` metadata，失败或空残件必须删除并转为 `failed`。

**验证：**

```sh
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'id=<已安装 iPhone Simulator UUID>'
make test-core
make test-apple-bridge
```

## 任务 4：实现本地回放与 SwiftUI 录音页面

**文件：**
- 新建：`apple/App/Audio/AppleAudioPlayback.swift`
- 新建：`apple/Shared/Recording/RecordingViewModel.swift`
- 新建：`apple/App/Features/Recording/RecordingView.swift`
- 新建：`apple/AppTests-iOS/RecordingViewModelTests.swift`
- 新建：`apple/AppTests-macOS/RecordingViewModelTests.swift`
- 修改：`apple/App/JKMeetingApp.swift`

**先失败的测试：**

1. fake capture 成功时，断言 view model 进入 `recording`、结束后显示时长与回放入口。
2. fake playback 失败时，断言保留录音并显示可读错误。
3. 中断事件发生时，断言停止采集、保留有效文件并展示“录音被中断”。

**最小实现：**

- 以单一按钮实现开始/停止，以独立按钮实现播放/停止。
- 页面展示权限、录音状态、文件 metadata 与错误；不加入上传或转写 UI。
- 播放实现通过协议隔离，避免 SwiftUI 直接操作 `AVAudioPlayer`。

**验证：**

```sh
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild test -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'id=<已安装 iPhone Simulator UUID>'
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-macOS -destination 'platform=macOS'
xcodebuild build -project apple/JKMeeting.xcodeproj -scheme JKMeeting-iOS -destination 'id=<已安装 iPhone Simulator UUID>'
```

## 任务 5：真实设备 smoke 与 P0 指标衔接

**文件：**
- 修改：`docs/metrics.md`
- 修改：`docs/plans/2026-09-29-p0-minimal-loop-design.md`
- 修改：`docs/项目知识复习.md`

**执行：**

1. 在 Mac 本机授权麦克风，录制 3 至 10 秒语音，确认 m4a 可解码回读、可播放、core metadata 合理；记录队列深度、丢帧与中断/错误。
2. 在 iPhone 真机执行同一流程；记录设备、系统版本、输入格式、采样率、声道数、文件大小、队列深度、丢帧、事件与失败信息。
3. 验证中断后的有效文件保留且可回放、无效残件被清理；验证 5 分钟上限自动停止。
4. 不以两个设备的原始波形相同为验收；只要求同一 core 输入有确定性输出、两端真实链路都能录制回放。
5. 记录 core 处理耗时与采集时长比例；上传、ASR 与松手到文本指标留在第 6 步客户端 API 对接后测量。

**验证：**

```sh
make test-core
make test-apple-bridge
xcodegen generate --spec apple/project.yml --project apple
git diff --exit-code -- apple/JKMeeting.xcodeproj
```

## 交付门禁

独立评审 agent 必须确认：平台边界没有进入 C++ core；双端测试 target 完整；文件格式与 CoreProcessor 的输入输出兼容；权限、中断、落盘失败和播放失败可见；真机 smoke 没有被 Simulator 成功替代。仅评审通过后开始编码。
