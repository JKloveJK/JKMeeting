# JKMeeting Apple 平台壳层

`apple/` 是 P0 的 iOS 与 macOS 共享平台壳层：它负责把两端的音频采集、文件与网络能力接到共享 `core/`，不在此处复制跨平台音频算法。

## 当前桥接基线

`Bridge/JKMeetingCore.h` 暴露 Foundation 形式的 PCM 重采样接口：

- 输入为交错 S16LE PCM 的 `NSData`，桥接层校验字节长度、采样率与声道数。
- 桥接层复制输入以确保传给 C++ 的 `int16_t*` 对齐且只在调用期间有效。
- 输出为不可变的 `JKMeetingAudioFrame`，携带拥有型 `NSData`、采样率、声道数和时间戳。
- 该层不放采集、播放、文件 IO 或网络逻辑；它只负责 Objective-C / C++ 的类型与生命周期转换。

已建立 iOS/macOS 的 XcodeGen 工程和共享采集、回放、手动上传转写代码；macOS Foundation + ObjC++ CMake 测试验证 bridge 可调用同一份 `core/`。模拟器短录音经服务端异步转写已有一次闭环记录；Mac 和 iPhone 物理设备录音、网络与回放仍待单独验收。

## 测试

从仓库根目录运行：

```sh
make test-apple-bridge
```

该命令构建 `apple/Bridge/JKMeetingCore.mm`、链接 `core/`，并运行正常 PCM 重采样及非法字节长度拒绝测试。
