# JKMeeting C++ core

`core/` 是 iOS 与后续 Android 共享的纯 C++ 音频处理模块。它只包含纯计算、帧数据和状态逻辑；不依赖平台 SDK，不包含采集、播放、文件 IO、网络或 UI。

## P0 契约

- 输入：`AudioFrameView` 借用调用方的交错 S16LE PCM；输入缓冲区只需在本次调用期间有效。
- 输出：`OwnedAudioFrame` 用 `std::vector<int16_t>` 拥有 PCM；返回后可安全交给 ObjC++ / JNI 桥接层继续使用。
- 当前能力：最近邻重采样与 dB 增益，增益结果会饱和裁剪至 `int16_t` 范围。
- 无效输入（空 PCM、非正采样率或声道数、不能被声道数整除的采样数）返回空输出帧。

完整所有权决策见 [`docs/decisions/0002-core-audio-frame-ownership.md`](../docs/decisions/0002-core-audio-frame-ownership.md)。

## 运行与测试

从仓库根目录执行：

```sh
make test-core
```

该命令使用 CMake 配置、构建并通过 CTest 运行单元测试。
