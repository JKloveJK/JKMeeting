# 0002：C++ core 使用借用输入帧与拥有型输出帧

- 日期：2026-09-29
- 状态：已接受
- 关联：`docs/plans/2026-09-29-p0-minimal-loop-design.md` §3.6、`AGENTS.md`

## 背景

P0 的 C++ core 需要接收来自 iOS `AVAudioEngine`、后续 Android `AudioRecord` 的 PCM，并输出经重采样和增益处理后的 PCM。原始设计中的 `AudioFrame` 用裸指针表示 PCM，且 `resample()` 同样返回裸指针帧，没有说明输出缓冲区由谁创建、何时释放、是否可以跨越桥接调用保存。

该歧义会使 ObjC++ / JNI 桥接面临悬空指针、重复释放或隐式拷贝，并且无法通过 API 类型表达生命周期。

## 决定

- 输入使用 `AudioFrameView`：`const int16_t* samples` 仅为借用视图；调用者拥有输入缓冲区，且其生命周期至少覆盖本次 core 调用。
- 输出使用 `OwnedAudioFrame`：以 `std::vector<int16_t>` 持有 PCM；core 返回值拥有输出数据，调用方可安全读取、移动或写入平台文件。
- P0 的 `AudioProcessor::resample()` 接收 `AudioFrameView`，返回 `OwnedAudioFrame`；`apply_gain()` 仅修改 `OwnedAudioFrame`。
- P0 采用交错、S16LE PCM，`sample_count` 表示总采样值数量而不是每声道帧数。

## 依据

输入借用避免在实时采集路径对平台缓冲区做不必要的预拷贝；输出拥有型容器消除返回裸指针的释放责任歧义。两个类型将生命周期写入 API，使 macOS 单测、ObjC++ 和后续 JNI 具有同一语义。

## 后果

- 重采样必然分配输出缓冲区；P0 优先保证正确性与桥接清晰，不在此阶段引入调用方缓冲池。
- `AudioFrameView` 不可在调用结束后保存；若未来需要异步处理，必须显式复制或引入独立的所有权 API。
- 性能基线应记录 core 耗时占比；若 P2 证明分配成为瓶颈，再通过新的 ADR 评估预分配或调用方提供缓冲区的方案。
