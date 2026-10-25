# P3-1 LLM 结构化纪要与评测技术方案

- 日期：2026-10-23
- 状态：已实施，待用户终审（结果见 [P3-1 报告](../reports/2026-10-23-p3-1-insights-report.md)）
- 需求：[P3 Agent 管道需求 · P3-1](../requirements/2026-10-23-p3-agent-pipeline-requirements.md)

## 1. 目标

转写完成后，异步调用本机 LLM，产出符合 JSON Schema 的会议纪要；同时提供可复现的评测工具，量化召回率、格式稳定性、延迟和 token 消耗。

## 2. 关键取舍

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| 推理服务 | `llama-server`，使用 OpenAI 兼容接口，端口 `8083`，`-c 8192 -np 1`，Metal 全量卸载 | 本机已安装 llama.cpp；接口兼容，以后换外部 API 只需改地址 |
| 格式稳定 | 请求时带 `response_format: {type: json_schema}`（llama.cpp 会把 schema 转成 GBNF 语法约束解码），返回后在 Go 侧再做一次 schema 校验 | 约束解码从源头保证语法；Go 侧校验兜住语义约束（例如必填字段为空串），两层防线 |
| 触发 | `MarkDoneWithLease` 在同一事务内写入 `meeting_insights(pending)` 和 `analysis_outbox` | 复用 P1/P2-4 的事务 outbox，保证“转写完成”和“待分析”原子一致 |
| 投递 | API 进程的 dispatcher 后台循环投递 `analysis` 队列 | outbox 行由 Worker 写入，不经过同步派发；后台循环 500 ms 一次，延迟可接受 |
| 消费 | Worker 中第三个 `asynq.Server`，队列 `analysis`，并发 `LLM_CONCURRENCY`（默认 1） | 与转写、转码隔离；本机只有一个推理槽位，并发再高只会排队 |
| 上下文预算 | 按 rune 数保守估算 token 数（中文约 1 字 1 token）。转写预算为 `LLM_CONTEXT_TOKENS − 系统提示 − 输出预留`；超出时按句子切段，先逐段抽取，再合并 | 不静默截断；估算偏保守，宁可多切一段 |
| 准确率 | 规则匹配要点召回（关键词 + 负责人），不用 LLM 评判 | 可复现，避免“模型给自己打分” |

## 3. 数据模型（迁移 `0004_add_meeting_insights.sql`）

- `meeting_insights`：
  - `recording_id`（主键，外键级联删除）；
  - `status`（`pending` / `processing` / `ready` / `failed`），租约字段 `attempts`、`lease_token`、`lease_until`；
  - 结果字段：`result`（JSONB）、`model`、`prompt_tokens`、`completion_tokens`、`llm_calls`、`analysis_ms`；
  - `error_message`、时间戳。
- `analysis_outbox`：列与 `transcription_outbox` 相同。
- 历史录音没有分析行，接口返回 `none`。

## 4. 结构化结果

```json
{
  "summary": "string，3～5 句",
  "decisions": ["string"],
  "action_items": [{"task": "string", "owner": "string，可为空", "due": "string，可为空"}],
  "viewpoints": [{"speaker": "string", "points": ["string"]}]
}
```

Go 侧校验：`summary` 不能为空；`decisions`、`action_items`、`viewpoints` 可以是空数组，但必须存在；`action_items[].task` 和 `viewpoints[].speaker` 不能为空。

## 5. 模块

- `server/internal/llm`：`Client.ChatJSON(ctx, messages, schema, validate)`。
  - 负责并发信号量、超时、重试分类（超时 / 网络 / 5xx / 429 / 格式错误可重试，4xx 不重试）和指数退避；
  - 返回内容、token 用量、尝试次数与耗时；
  - 不依赖具体业务。
- `server/internal/insights`：
  - `schema.go`：结构体、JSON Schema、`Validate`；
  - `extractor.go`：提示词、预算切分、合并调用，汇总每次调用的 token 与耗时；
  - `repository.go`：领取租约、标记就绪 / 失败、查询；
  - `handler.go`：asynq 任务处理（领取 → 读取转写 → 抽取 → 写库）。LLM 不可达或重试耗尽时释放租约交给 asynq 重试，第 3 次仍失败则标记 `failed`。
- `server/internal/httpapi`：`GET /v1/recordings/{id}/insights`。
- `server/cmd/insighteval`：评测工具。
  - 文本模式：直接调用 `insights.Extractor`，与 Worker 是同一代码路径；
  - `-repeat` 控制总调用次数，用于 E4 的 50 次连续调用；
  - 输出 JSONL 和汇总。
- `server/eval/p3/cases/*.json`：评测集，包含转写文本和带关键词的标准答案。
- `server/scripts/p3-audio-eval.sh`：音频模式，用 `say` 合成后走完整上传链路，等待 insights 就绪后交给评测工具打分。

## 6. 配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LLM_BASE_URL` | `http://127.0.0.1:8083` | llama-server 地址 |
| `LLM_MODEL` | `qwen2.5-7b-instruct-q4_k_m` | 写入结果的模型标识 |
| `LLM_TIMEOUT_SECONDS` | `90` | 单次调用超时（秒） |
| `LLM_MAX_RETRIES` | `2` | 单次逻辑调用的额外重试次数 |
| `LLM_CONCURRENCY` | `1` | 客户端并发上限与 analysis 队列并发 |
| `LLM_CONTEXT_TOKENS` | `8192` | 与 `llama-server -c` 一致 |

## 7. 错误处理

| 场景 | 处理 |
| --- | --- |
| 转写为空 | 直接 `ready`，结果为空摘要的固定结构，不调用模型 |
| LLM 超时、5xx、格式错误 | 客户端内部重试；耗尽后返回错误，handler 释放租约交给 asynq 重试 |
| 第 3 次任务仍失败 | `failed`，记录错误信息；转写状态不受影响 |
| 4xx（如请求过大） | 不重试，直接 `failed` |

## 8. 测试

| 层 | 测试 |
| --- | --- |
| `llm` | `httptest` fake：超时、500 后成功、格式错误后成功、400 不重试、重试耗尽、并发上限、token 统计 |
| `insights` | 校验规则；预算切分（长文本触发 map-reduce，调用次数与合并结果）；handler 状态转换（fake 依赖） |
| `recording` | `MarkDoneWithLease` 同事务写入分析行和 outbox（带数据库时运行） |
| `httpapi` | insights 接口的 `none` / `pending` / `ready` / `failed` |
| 评测 | `go run ./cmd/insighteval`：文本模式 20 条和 50 次连续调用；音频模式 5 条 |

## 9. 实施步骤

1. 迁移 0004、`llm` 包与测试；
2. `insights` 包与测试；
3. 接入 recording 仓库、Worker、API 和配置；
4. 评测集与评测工具；
5. 启动 `llama-server` 运行评测，写报告并更新指标。

## 10. 实施修正

- 评测集改为单文件 `server/eval/p3/cases.json`（20 条数组），不再一条一个文件，方便评审答案时一次看完。
- 关键词支持 `|` 分隔的同义写法（如 `周三|星期三`），比较前去掉空白和标点并统一小写；评分逻辑放在 `insights/eval.go`，有单测覆盖。
- 超时配置名改为 `LLM_TIMEOUT_SECONDS`（整数秒，默认 90），与现有 `*_MS` 整数配置风格一致；`LLM_MAX_RETRIES` 允许为 0。
- `MarkDoneWithLease` 新增 `requestID` 参数，`analysis_outbox.request_id` 沿用转写任务的 `request_id`，让日志能串起转写和纪要两个阶段。租约过期时不写分析行；两条插入都用 `ON CONFLICT DO NOTHING` 保持幂等。
- analysis dispatcher 只在 API 进程里跑后台循环，不加入 `complete` 的同步派发：`complete` 不会产生 analysis 行。
- 音频模式脚本把 `姓名：` 改写为 `姓名说，` 再合成。`say` 只有一个音色，转写结果不带说话人标签，只能靠话里的人名提供线索。
