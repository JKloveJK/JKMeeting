# P0 设计：最小端到端闭环（采集 → C++ core → 上传 → 存储 → 转写 → 回放）

- 日期：2026-09-29（架构修正版）
- 阶段：P0（预估 **4 周** —— 因 C++ core + 桥接与 ASR 同时在 P0）
- 关联文档：[客户端全栈学习路线](../客户端全栈学习路线.md)
- 状态：**待评审**

---

## 0. 决策记录

| 项 | 决定 | 理由 |
| --- | --- | --- |
| 服务端语言 | **Go** | 语法量小、单二进制部署、云原生生态最成熟 |
| 安卓端语言 | **Kotlin + Compose**（P4 开工） | 用户明确：**上层只负责绘制 UI** |
| 共享层 | **C++ core，从 P0 就存在** | 用户目标即跨端抽象能力，且这是其日常工作（WeMeet）的架构 |
| 学习重心 | **音视频** | 用户明确；服务端 / Agent / 安卓为配套能力 |
| 音频入口 | **iOS 实时录音** | 用户明确：先要真实产品形态 |
| 采集方式 | **AVAudioEngine + tap**（非 AVAudioRecorder） | PCM 必须先经过 core，见 §3.6 |
| 上传时机 | **录完整段后一次 PUT** | 不做边录边传；流式留到 P5 |
| 录音时长 | **≤ 5 分钟，前台录音** | 后台录音、断点续传推到 P1 |
| 服务端位置 | **本地 Docker Compose** | P0 的敌人是"动不起来"，不是"不够真实" |
| ASR | **本机 whisper.cpp（Metal）** | 零成本、离线、可压出真实 RTF 并调优 |
| iOS UI | **SwiftUI** | 与 Compose 心智同源，P4 双端对比时价值最大 |
| 编解码（P0） | **平台原生**（AudioConverter / MediaCodec） | 零交叉编译负担，见 §3.6 |

---

## 1. 一句话范围

在 iPhone 上录一段 ≤5 分钟音频 —— PCM 先经 C++ core 处理再落文件 —— 松手后上传到本机 Docker 里的 Go 服务，落进 MinIO，DB 留一条记录，异步转写出文字，手机立刻能回放音频并看到转写结果。

---

## 2. 主链路

```
1. 采集     AVAudioEngine 的 tap 抓到 PCM buffer（16kHz 单声道）
2. 处理     ObjC++ 桥接 → C++ core：帧管理 / 重采样 / 增益
3. 落文件   core 输出 → AVAudioFile 写 .m4a（内部走 AudioConverter）
4. 创建     POST /v1/recordings            → recording_id + 预签名上传 URL
5. 上传     PUT  <预签名 URL>               → 字节直达 MinIO，服务端不代理
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
│      │      ──▶ [minio:9000]   bucket: recordings     │
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
├── ios/
│   ├── JKMeeting.xcodeproj
│   └── JKMeeting/
│       ├── Bridge/          # ObjC++：JKMeetingCore.h / .mm
│       ├── Audio/{CaptureEngine.swift, PlaybackController.swift}
│       ├── Network/{APIClient.swift, Models.swift}
│       └── Views/{RecordingView.swift, TranscriptView.swift}
├── android/                 # P4 才填
├── server/                  # Go：module github.com/<账号>/JKMeeting/server
│   ├── go.mod
│   ├── cmd/{api,worker}/main.go
│   ├── internal/recording/{handler.go, service.go, store.go, model.go}
│   ├── internal/transcribe/{transcriber.go, whisper_http.go}
│   ├── internal/platform/{db.go, minio.go, queue.go, config.go, logging.go}
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

```cpp
namespace jkmeeting::audio {

struct AudioFrame {
    const int16_t* samples;   // PCM，S16LE
    size_t         count;
    int            sample_rate;
    int            channels;
    int64_t        timestamp_ms;
};

class AudioProcessor {
public:
    // P0：只做重采样 + 增益。不引任何第三方库。
    AudioFrame resample(const AudioFrame& in, int target_rate);
    void       applyGain(AudioFrame& frame, float gain_db);
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
| **ObjC++ 桥接** | 真机跑通一次调用 + 打印返回值 | 同一个 core 二进制在 macOS 与 iOS 上行为一致 |
| Go 接口 | 表驱动单测 + `testcontainers-go` 跑真 Postgres/MinIO | 4 个接口的正/异常路径全覆盖 |
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

| # | 步骤 | 时间盒 | 成功判据 |
| --- | --- | --- | --- |
| 1 | **C++ core + 桥接打通** | **3 天** | macOS 上 `ctest` 通过；iOS 真机成功调用同一个函数并打印正确结果 |
| 2 | 起底座 | 1 天 | `docker compose up` 后 postgres / redis / minio 三个服务健康 |
| 3 | 通接口 | 2–3 天 | 4 个接口用 curl 跑通完整管道（不需要 iPhone） |
| 4 | 通 ASR | 1–2 天 | 宿主机跑起 whisper.cpp server，curl 转一段音频，量出第一次 RTF |
| 5 | 通采集 | 2–3 天 | iOS 用 AVAudioEngine 拿到 PCM，经 core 处理后落 m4a，能本地回放 |
| 6 | 对接 | 2–3 天 | 端到端跑通，报出四个基线数 |

**第 1 步的时间盒是硬的。** 3 天（约 12 小时投入）内没打通 CMake + ObjC++ 桥接，就**先绕过去** —— 采集后直接写文件、不走 core，继续第 2–4 步，把 core 接入推到 P1。理由：桥接是纯工程体力活，卡住它不该拖住整条链路；而且它失败的信号很明确，不会影响后面的判断。

**砍单规则（P0 超期时按序砍）**：

1. 先砍 ASR 的 iOS 展示（只跑通服务端侧，UI 后补）
2. 再砍 core 的接入（退回采集后直接写文件）
3. **永远不砍 core 的构建链路本身** —— 那是跨端方案能不能成立的唯一验证

第 1 步和第 3、4 步互不依赖，可以并行推进；第 3、4 步完全不依赖 iPhone —— 这两步做完，云侧的 80% 不确定性就消掉了。
