# P0 设计：最小端到端闭环（采集 → C++ core → 上传 → 存储 → 转写 → 回放）

- 日期：2026-09-29（架构修正版）
- 阶段：P0（预估 **4 周** —— 因 C++ core + 桥接与 ASR 同时在 P0）
- 关联文档：[客户端全栈学习路线](../客户端全栈学习路线.md)
- 状态：**实施中**（截至 2026-10-04：服务端合成音频经上传、ASR 和回放 URL 的本机链路已复验；Apple App 已有手动转写入口并在模拟器注入运行时配置，真实录音从 App 上传并显示文字、物理设备验收与四项基线尚未验证）

---

## 协作与证据约束

P0 的实现必须遵守根目录 [AGENTS.md](../../AGENTS.md)：先声明目标、非目标与验证方式，再以小改动完成并保留可复现证据。当前 P0 的性能与质量结果统一登记在 [指标台账](../metrics.md)；会影响跨模块边界、状态机、数据模型或后续阶段的取舍，必须新增 [ADR](../decisions/README.md)。

当前 `make verify` 已包含 core、ObjC++ bridge 与 Go 单测；Apple 双端 XCTest 和服务端/ASR 冒烟仍须单独执行。`make e2e-local`、`make load` 仍为占位目标，不能用 `make verify` 成功代替真实录音或端到端验收。

## 当前执行状态

| P0 步骤 | 状态 | 已验证证据 | 剩余边界 |
| --- | --- | --- | --- |
| 1. C++ core + Apple bridge | **基础实现已完成** | `make test-core`、`make test-apple-bridge`、双端 Xcode 构建及 `CoreProcessor` 确定性单测通过 | 真实麦克风 PCM 经 bridge/core 的设备验证归步骤 5 |
| 2. 本地服务底座 | **已完成（既有验证）** | PostgreSQL、Redis、SeaweedFS 有 `healthy` 验证记录 | 启动端到端测试前需重新确认服务健康 |
| 3. Go 录音 API | **已完成 P0 本地接口** | migration、创建、预签名 PUT/GET、complete、查询、回放 URL 与 API 冒烟通过 | 固定 Bearer token 仅限 P0；回放 URL 可获取不等于客户端已接入 |
| 4. 异步 ASR | **已完成服务端正常链路** | Whisper base、`whisper-server`、asynq Worker；真实 `.m4a` 从 `uploaded` 进入 `done` 并写入文本 | outbox 认领后进程崩溃时，`dispatching` 事件尚无自动回收路径；RTF 基线未测 |
| 5. Apple 采集、落盘与回放 | **进行中（实现与单测就绪，验收未完成）** | 双端采集、原生 PCM 转换、core pipeline、AAC/m4a 文件、本地播放与 SwiftUI 已有代码；iOS 26.3 Simulator 与 macOS XCTest 各 20 项通过；用户反馈模拟器中格式崩溃看上去已消失 | Mac 本机与 iPhone 真机完成录制→回读→播放 smoke；实现并验证 5 分钟自动停止、平台中断与文件收尾、权限拒绝引导；核对停止时尾帧及处理队列有界性 |
| 6. 客户端对接与端到端基线 | **进行中（代码已接入，用户流未验收）** | App 有手动“上传并转写”入口及创建、PUT、complete、轮询实现；2026-10-04 模拟器进程已注入本机 API/S3 配置，合成音频从服务端走到 `done`，预签名 GET 返回音频 | 请在已配置的模拟器重新录音并点击上传以观察 UI 文本；Mac/iPhone 实机、断网重试、四项指标基线仍待完成；预签名远程回放只在服务端验证，本地录音直接播放 |

> 当前本地 S3 兼容对象存储采用 SeaweedFS，详见 ADR 0004；不再使用原计划中的 MinIO。

## 0. 决策记录

| 项 | 决定 | 理由 |
| --- | --- | --- |
| 服务端语言 | **Go** | 语法量小、单二进制部署、云原生生态最成熟 |
| 安卓端语言 | **Kotlin + Compose**（P4 开工） | 用户明确：**上层只负责绘制 UI** |
| 共享层 | **C++ core，从 P0 就存在** | 用户目标即跨端抽象能力，且这是其日常工作（WeMeet）的架构 |
| 学习重心 | **音视频** | 用户明确；服务端 / Agent / 安卓为配套能力 |
| Apple 客户端 | **iOS + macOS 双端** | 用户明确：两个平台均为 P0 一等客户端，复用同一份 SwiftUI、ObjC++ bridge 与 C++ core |
| 音频入口 | **Apple 平台实时录音** | 双端壳层已有 PCM 采集与确定性测试；Mac 本机、iPhone 真机录音回放待验收 |
| 采集方式 | **AVAudioEngine + tap**（非 AVAudioRecorder） | PCM 必须先经过 core，见 §3.6 |
| 上传时机 | **录完整段后一次 PUT** | 不做边录边传；流式留到 P5 |
| 录音时长 | **≤ 5 分钟，前台录音** | 后台录音、断点续传推到 P1 |
| 服务端位置 | **本地 Docker Compose** | P0 的敌人是"动不起来"，不是"不够真实" |
| ASR | **本机 whisper.cpp（Metal）** | 零成本、离线、可压出真实 RTF 并调优 |
| iOS UI | **SwiftUI** | 与 Compose 心智同源，P4 双端对比时价值最大 |
| 编解码（P0） | **平台原生**（AudioConverter / MediaCodec） | 零交叉编译负担，见 §3.6 |

---

## 1. 一句话范围

在 iPhone 或 Mac 上录一段 ≤5 分钟音频 —— PCM 先经 C++ core 处理再落文件 —— 松手后上传到本机 Go 服务，落进 SeaweedFS S3 兼容对象存储，DB 留一条记录，异步转写出文字，客户端立刻能回放音频并看到转写结果。

Apple 双端工程、确定性 PCM 自检、采集与本地回放代码均已落地；真实设备录音/回读/回放 smoke 与客户端服务端对接尚未验收，详见 [ADR 0003](../decisions/0003-apple-multiplatform-shell.md) 和“当前执行状态”。

---

## 2. 主链路

```
1. 采集     AVAudioEngine 的 tap 使用设备原生格式抓取 PCM；壳层转换为交错 S16LE 单声道
2. 处理     ObjC++ 桥接 → C++ core：帧管理 / 重采样到 16 kHz / 增益
3. 落文件   core 输出 → AVAudioFile 写 .m4a（内部走 AudioConverter）
4. 创建     POST /v1/recordings            → recording_id + 预签名上传 URL
5. 上传     PUT  <预签名 URL>               → 字节直达 SeaweedFS，服务端不代理
6. 确认     POST /v1/recordings/{id}/complete
              → Go HeadObject 校验对象存在 → 写 DB → 投递 asynq 任务
7. 轮询     GET  /v1/recordings/{id}        → transcribing → done + transcript
8. 回放     GET  /v1/recordings/{id}/playback → 预签名 GET URL
```

**两个关键取舍**：

1. **不能用 `AVAudioRecorder`。** 它直接把音频写进文件，你拿不到 PCM，也就无法在中间插 C++ 处理。必须用 `AVAudioEngine` + `installTapOnBus` 拿到 PCM buffer。这是"引入 C++ core"带来的第一个直接后果。
2. **预签名 URL 而非服务端代理字节。** 服务端不承载文件流量，带宽和内存不被文件流占用。

创建接口在**录音结束之后**调用：此时 `duration_ms` 与 `size_bytes` 已知，且避免录音中断留下孤儿记录。

---

## 3. 组件与接口

### 3.1 HTTPS 接口

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/recordings` | `{duration_ms, size_bytes, content_type}` | `{recording_id, upload_url, upload_expires_at}` |
| POST | `/v1/recordings/{id}/complete` | `{}` | `{recording_id, status}` |
| GET | `/v1/recordings/{id}` | — | `{recording_id, status, duration_ms, transcript, error_message}` |
| GET | `/v1/recordings/{id}/playback` | — | `{playback_url, expires_at}` |

鉴权：P0 用固定 Bearer token，完整用户体系在 P1。

### 3.2 数据模型

```sql
CREATE TABLE recordings (
    id            UUID PRIMARY KEY,
    user_id       TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    object_key    TEXT        NOT NULL DEFAULT '',
    duration_ms   INTEGER     NOT NULL,
    size_bytes    BIGINT      NOT NULL,
    content_type  TEXT        NOT NULL,
    transcript    TEXT,
    error_message TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recordings_user_created ON recordings (user_id, created_at DESC);
```

`status` 取值：`created` / `uploaded` / `transcribing` / `done` / `failed`。

### 3.3 状态机

```
created ──complete──▶ uploaded ──enqueue──▶ transcribing ──ok──▶ done
                                                │
                                          fail×3 └──▶ failed ──retry──▶ transcribing
```

所有跃迁用条件更新实现，非法跃迁一律拒绝：

```sql
UPDATE recordings SET status = 'transcribing', updated_at = now()
WHERE id = $1 AND status = 'uploaded';
```

以 `RowsAffected == 1` 判断状态是否真的发生转移 —— 这就是幂等的实现方式。

### 3.4 本地运行拓扑

```
iPhone（与 Mac 同一 Wi-Fi）
   │  http://<Mac 局域网 IP>:8080
   ▼
┌─────────────────── docker compose ────────────────────┐
│  [api: Go]  ──▶ [postgres:5432]                       │
│      │      ──▶ [seaweedfs:9000] S3 bucket: recordings│
│      │      ──▶ [redis:6379]   asynq 队列             │
│      ▼                                                │
│  [worker: Go] ──取任务──▶ redis                       │
└───────┬───────────────────────────────────────────────┘
        │  http://host.docker.internal:8081
        ▼
   whisper.cpp server（宿主机原生跑，Metal 加速）
```

`whisper.cpp server` 跑在宿主机而非容器内 —— macOS 容器拿不到 Metal。

### 3.5 目录结构（monorepo，每部分一个顶层目录）

```
JKMeeting/
├── core/                    # 纯 C++，CMake，可在 macOS 直接跑单测
│   ├── CMakeLists.txt
│   ├── include/jkmeeting/{audio_frame.h, processor.h}
│   ├── src/{audio_frame.cpp, processor.cpp}
│   ├── tests/               # GoogleTest
│   └── third_party/         # P2 之后放 opus
├── apple/                   # iOS + macOS 共享 Apple 平台壳层
│   ├── project.yml           # XcodeGen 唯一工程声明来源
│   ├── JKMeeting.xcodeproj
│   ├── Bridge/               # ObjC++：JKMeetingCore.h / .mm
│   ├── App/                  # 两端共享 SwiftUI 源码
│   ├── AppTests-iOS/         # iOS 独立 XCTest 源码
│   └── AppTests-macOS/       # macOS 独立 XCTest 源码
├── android/                 # P4 才填
├── server/                  # Go：module github.com/<账号>/JKMeeting/server
│   ├── go.mod
│   ├── cmd/{api,worker}/main.go
│   ├── internal/recording/{handler.go, service.go, store.go, model.go}
│   ├── internal/transcribe/{transcriber.go, whisper_http.go}
│   ├── internal/{storage,queue,config}/ # S3、队列、配置适配
│   └── migrations/0001_init.sql
├── docs/
├── docker-compose.yml
├── Makefile
├── AGENTS.md
└── .gitignore
```

> Go module 放 `server/` 而非仓库根 —— 因为 `core/CMakeLists.txt` 也要在根，两者放一起会打架。

**命名映射**：仓库名 `JKMeeting` 是大驼峰，而各语言对标识符的要求不同（GitHub 大小写不敏感、Go module path 里的大写会被转义、C++ namespace 习惯小写），因此同一项目在各语言里是不同写法，需一次定死：

| 用途 | 形式 |
| --- | --- |
| GitHub 仓库 | `JKMeeting`（GitHub 大小写不敏感，`jkmeeting` 指向同一个仓） |
| Go module | `github.com/<账号>/JKMeeting/server` |
| Go package | 按目录名（`recording` / `transcribe`），不用仓库名 |
| C++ namespace | `jkmeeting`（习惯小写） |
| C++ 头文件路径 | `include/jkmeeting/` |
| iOS target / module | `JKMeeting` |
| ObjC++ 桥接头 | `JKMeetingCore.h` / `.mm` |
| Kotlin / Android package | `com.<账号>.jkmeeting` |

**已接受的一处代价**：Go 模块代理会对 module path 里的大写字母做 case-encoding，`github.com/<账号>/JKMeeting/server` 在模块缓存目录里会显示成 `.../!j!k!meeting/server`。功能不受影响，只是排查依赖时看着别扭。将来若想消除，把仓库改成全小写即可 —— 但那要改所有 import，越晚越贵。

### 3.6 C++ core 的边界（本设计最关键的约束）

**core 只放「纯计算 + 纯状态机 + 协议」。任何碰平台 API 的东西留在壳层。**

判据一句话：**需要 `#include <AVFoundation>` 或 `<android/...>` 的，就不该进 core。**

| 进 core | 留在壳层 |
| --- | --- |
| 帧与时间戳管理（`AudioFrame`） | 音频采集（AVAudioEngine / AudioRecord） |
| 重采样、增益、静音检测 | 播放输出 |
| 编解码、Opus 封装 | 文件 IO、网络 socket |
| 状态机、协议序列化、参数校验 | UI 状态 |

壳层那五样**平台差异最大、共享收益最小、调试成本最高**。把它们塞进 core 的后果是：你会花几周写 `#ifdef` 分支，最后发现共享层比两份实现还难维护。

**core 的第一个 API（P0 只做这些）**：

输入帧与输出帧必须有明确的所有权：`AudioFrameView` 只在调用期间借用壳层提供的 PCM；`OwnedAudioFrame` 用 `std::vector<int16_t>` 持有 core 的输出。因此 ObjC++ / JNI 桥接层不能保存输入裸指针，`resample()` 的返回结果可安全跨越调用边界。详见 [ADR 0002](../decisions/0002-core-audio-frame-ownership.md)。

```cpp
namespace jkmeeting::audio {

struct AudioFrameView {
    const int16_t* samples;   // 借用的交错 PCM，S16LE；不负责释放
    size_t         sample_count;
    int            sample_rate;
    int            channels;
    int64_t        timestamp_ms;
};

struct OwnedAudioFrame {
    std::vector<int16_t> samples;  // core 拥有并负责其生命周期
    int                  sample_rate;
    int                  channels;
    int64_t              timestamp_ms;
};

class AudioProcessor {
public:
    // P0：只做重采样 + 增益；不引入第三方库。
    OwnedAudioFrame resample(const AudioFrameView& input, int target_rate) const;
    void apply_gain(OwnedAudioFrame& frame, float gain_db) const;
};

}  // namespace jkmeeting::audio
```

### 3.7 编解码三步走（把交叉编译风险往后推）

跨端 C++ 真正难的不是写 C++，是**第三方库的交叉编译**。FFmpeg 需要分别为 iOS（真机 arm64 + 模拟器）与 Android（arm64 + x86_64）编译，这一步能吞掉几周且学不到核心能力。

| 阶段 | 编解码方案 | 交叉编译负担 |
| --- | --- | --- |
| P0–P1 | 平台原生（AudioConverter / MediaCodec） | 零 |
| P2 之后 | Opus（纯 C，CMake 友好） | 低 |
| P5 | FFmpeg（视频 + 全格式） | 高，但那时你有能力处理 |

---

## 4. P0 明确不做（护栏）

| 不做 | 推到 |
| --- | --- |
| 登录注册、多用户 | P1（P0 用固定 token） |
| 后台录音、断点续传、边录边传 | P1 |
| 转码、HLS、实时流 | P5 |
| 摘要 / 待办 / RAG / MCP | P3 |
| 安卓端 | P4 |
| HTTPS、域名、云部署、K8s | P2 部署环节 |
| 完整 OpenTelemetry SDK | P2（P0 只用 `request_id` 贯穿日志） |
| 引入 Opus / FFmpeg | P2 / P5（P0 用平台原生编解码） |
| core 里的音频采集、播放、网络、文件 IO | **永不进 core** |

---

## 5. 错误处理与边界

| 环节 | 出错情形 | P0 处理 |
| --- | --- | --- |
| 采集 | 麦克风权限被拒 | 录音前弹窗；被拒时给引导去设置的文案，**不静默失败** |
| 采集 | 来电 / 音频会话中断 | 监听 interruption 通知，停止并保留已录部分 |
| core | 桥接层传入空 buffer / 非法采样率 | core 返回空帧而非崩溃；桥接边界做参数校验 |
| 上传 | 网络中断 / 超时 | 3 次指数退避重试；失败保留本地文件 + UI 给"重新上传" |
| 上传 | 预签名 URL 过期 | 有效期 15 分钟，过期重调创建接口 |
| complete | 客户端说谎（对象不存在） | Go 侧 `HeadObject` 校验，不存在直接 400，**不写 DB** |
| 队列 | worker 崩溃 / 任务丢失 | asynq at-least-once + 处理逻辑幂等 |
| ASR | whisper 服务不可达 | 重试 3 次后置 `failed` 并写 `error_message` |
| 并发 | 同一 recording 重复 complete | 状态机条件更新，重复调用返回当前状态而非报错 |

---

## 6. 测试与验收

| 层 | 怎么测 | 验收标准 |
| --- | --- | --- |
| **C++ core** | `ctest`，在 macOS 直接跑（不依赖 Xcode） | 重采样/增益的边界用例（空帧、单帧、非法采样率）全通过 |
| **ObjC++ 桥接** | 先跑 `make test-apple-bridge`（macOS Foundation + ObjC++ 调同一份 core），再完成 iPhone 与 Mac 的真实调用 | macOS bridge 的正常/非法 PCM 用例通过；后续同一份 core 在 iPhone 与 Mac 上调用正确 |
| Go 接口 | 表驱动单测 + 本地 PostgreSQL / SeaweedFS / Redis 冒烟 | 4 个接口的正/异常路径全覆盖 |
| 状态机 | 单测穷举非法跃迁 | 非法跃迁一律被拒 |
| 上传管道 | curl + 预签名 URL 模拟客户端 | **不依赖 iOS 就能验完整管道**，P4 安卓直接复用 |
| iOS | 真机自测 + 手动断网注入 | 断网后能重传成功 |
| 端到端 | 录 30 秒 → 等出文字 | 松手到出文字 < 15 秒（whisper base） |

**P0 结束时必须能报出的四个基线数**：

1. 上传 P95 耗时（1.2MB / 局域网）
2. 转写 RTF（音频时长 ÷ 转写耗时）
3. 松手 → 屏幕上出文字的总时长
4. **core 处理耗时占采集总时长的比例**（决定后面能否上实时）

没有基线，后续所有"优化"都是自说自话。

---

## 7. 已知风险

| 风险 | 说明 | 缓解 |
| --- | --- | --- |
| **C++ 跨端构建** | CMake + Xcode 集成是全新链路，最容易卡死 | 见 §8 的时间盒；先做 macOS 命令行编译，再嵌 Xcode |
| **P0 过重** | core + 桥接 + 录音 + 服务端 + ASR 四件事同时上 | 见 §8 的砍单规则 |
| 环境不一致 | whisper 在宿主机、其余在容器 | 配置项 `ASR_BASE_URL` 隔离，P2 可换云 API 或容器 |
| `host.docker.internal` | macOS Docker Desktop 专有，Linux 写法不同 | 写进配置，不硬编码 |
| ATS 明文例外 | iOS 默认拒绝 HTTP，需要 Info.plist 例外 | 仅开发用，P2 上 HTTPS 后立即移除 |
| 5 分钟硬上限 | 录音模块的显式约束 | P1 接后台录音时一并放开 |

---

## 8. 下一步执行顺序

排序原则有两条：**最大不确定性优先**（早失败早调整）、**每步可独立验证**。

| # | 步骤 | 状态 | 成功判据 / 当前证据 |
| --- | --- | --- | --- |
| 1 | **C++ core + 桥接打通** | **基础实现已完成** | macOS CTest、ObjC++ bridge、双端 Xcode 构建与 `CoreProcessor` 单测通过；设备真实采集调用归步骤 5 |
| 2 | 起底座 | **已完成（既有验证）** | PostgreSQL、Redis、SeaweedFS 已有 `healthy` 记录；集成验收前复核运行状态 |
| 3 | 通接口 | **已完成 P0 本地接口** | 4 个接口通过 curl 跑通创建、直传、确认、查询与预签名回放 URL |
| 4 | 通 ASR | **已完成服务端正常链路** | Worker 处理真实 `.m4a` 并持久化转写文本；outbox `dispatching` 崩溃恢复待补 |
| 5 | 通采集 | **进行中（代码与单测就绪）** | 双端 AVAudioEngine→core→m4a→本地播放代码已具备，iOS 26.3 Simulator/macOS XCTest 各 20 项通过；尚需 Mac/iPhone 真实设备 smoke、5 分钟自动停止、中断收尾和队列边界验证 |
| 6 | 对接 | **进行中（客户端已编码，端到端待验收）** | App 已有手动上传、complete、文本轮询与本地回放；本机服务合成音频 ASR 冒烟通过，模拟器已按进程注入本机配置；用户录音→App 上传→文字、双端实测和四项基线仍待验证 |

**第 1 步的时间盒是硬的。** 3 天（约 12 小时投入）内没打通 CMake + ObjC++ 桥接，就**先绕过去** —— 采集后直接写文件、不走 core，继续第 2–4 步，把 core 接入推到 P1。理由：桥接是纯工程体力活，卡住它不该拖住整条链路；而且它失败的信号很明确，不会影响后面的判断。

**砍单规则（P0 超期时按序砍）**：

1. 先砍 ASR 的 iOS 展示（只跑通服务端侧，UI 后补）
2. 再砍 core 的接入（退回采集后直接写文件）
3. **永远不砍 core 的构建链路本身** —— 那是跨端方案能不能成立的唯一验证

第 1 步和第 3、4 步互不依赖，可以并行推进；第 3、4 步完全不依赖 iPhone —— 这两步做完，云侧的 80% 不确定性就消掉了。
