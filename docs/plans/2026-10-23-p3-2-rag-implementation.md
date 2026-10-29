# P3-2 转写向量索引与跨会议问答技术方案

- 日期：2026-10-23
- 状态：已实施，初步评测完成（见 [P3-2 报告](../reports/2026-10-29-p3-2-rag-report.md)）；`make eval-rag` 入口已于 2026-10-29 补齐
- 需求：[P3 Agent 管道需求 · P3-2](../requirements/2026-10-23-p3-agent-pipeline-requirements.md)

## 1. 关键取舍

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| 向量库 | pgvector（镜像 `pgvector/pgvector:pg16`），HNSW 索引，`vector_cosine_ops` | 用户已确认；与业务数据同库，同一事务写入，不新增服务 |
| 镜像切换 | 先 `pg_dump` 备份到被忽略的 `output/`，再换镜像沿用原数据卷，最后 `REINDEX DATABASE` | 原镜像基于 Alpine（musl，`en_US.utf8` 实际按字节序排序），新镜像基于 Debian（glibc）。排序规则不同，文本索引可能和数据顺序不一致，需要重建 |
| Embedding 服务 | 第二个 `llama-server` 进程：`--embeddings`，端口 `8084`，加载 bge-m3 Q8_0 | 和 LLM 共用 llama.cpp，接口是 OpenAI 兼容的 `/v1/embeddings` |
| 切块 | 按句子边界（`。！？；` 和换行）累积，每块不超过 300 字，相邻块重叠 1 句 | 中文会议句子短，300 字远低于 bge-m3 的 8192 上下文和默认 ubatch 512；重叠避免答案被切断 |
| 触发 | `MarkDoneWithLease` 的事务里再写一行 `transcript_index_jobs(pending)` 和一行 `index_outbox` | 和 P3-1 相同的 outbox 模式；索引和纪要互不阻塞 |
| 问答 | 在 API 进程里同步执行：问题向量化 → SQL 检索 top-k → LLM 用 `json_schema` 生成 `{answer, citations[], insufficient}` → 只保留属于检索结果的引用 | 问答是交互请求，排队异步化没有意义；校验引用防止模型编造来源 |
| LLM 并发 | API 进程有独立的 `llm.Client`（信号量为 1）；和 Worker 共用 `llama-server` 的 1 个槽位，由服务端排队 | 不新增跨进程协调；文档说明问答可能排在纪要任务后面 |

## 2. 数据模型（迁移 `0005_add_transcript_chunks.sql`）

- `CREATE EXTENSION IF NOT EXISTS vector`。
- `transcript_index_jobs`：列与 `meeting_insights` 的状态、租约部分相同，另加 `chunks`、`embed_ms`、`error_message`。
- `index_outbox`：列与 `analysis_outbox` 相同。
- `transcript_chunks`：
  - `id BIGSERIAL`；
  - `recording_id`，外键级联删除；
  - `chunk_index`、`content`；
  - `embedding vector(1024)`；
  - `UNIQUE(recording_id, chunk_index)`；
  - HNSW 索引，`vector_cosine_ops`。

## 3. 模块

- `internal/embedding`：`Client.Embed(ctx, []string) ([][]float32, error)`。负责超时、重试分类（复用 `llm.StatusError` 和 `llm.IsRetryable`）、维度校验，并做 L2 归一化。
- `internal/rag`：
  - `chunker.go`：`Split(text, maxRunes)`；
  - `repository.go`：索引任务的领取 / 释放 / 失败 / 就绪；`ReplaceChunks` 在同一事务内先删后写；`Search(vector, k)`；
  - `indexer.go`：asynq handler；
  - `answerer.go`：`Ask(ctx, question, k)`，包括提示词、schema 和引用校验。
- `internal/httpapi/ask.go`：`POST /v1/ask`。问题长度 1～500 字；`top_k` 取 1～10；索引为空时返回 `insufficient=true`，不调用 LLM。
- `cmd/rageval`：
  - 把 `cases.json` 中的 20 段文字稿以确定性 UUID（v5）写入 `recordings`（`object_key` 前缀为 `eval/p3-2/`，状态 `done`）；
  - 直接调用同一个 Indexer 和 Answerer；
  - 输出命中率、引用合法性、关键词命中率、资料不足判定和延迟。

## 4. 错误处理

| 场景 | 处理 |
| --- | --- |
| embedding 服务不可达 / 5xx | 索引任务释放租约后重试，第 3 次失败标记 `failed`；问答返回 `503` |
| 向量维度不是 1024 | 不重试，直接失败（属于配置错误） |
| LLM 返回不存在的引用 | 丢弃该引用并记录日志；全部无效且答案非空时，标记 `insufficient=true` |
| LLM 超时 | 问答返回 `504`，记录日志 |

## 5. 测试

- `embedding`：fake 服务验证归一化、维度错误、5xx 重试。
- `rag`：切块（边界、重叠、超长句硬切）；引用校验；索引 handler 的状态转换（fake 依赖）。
- 数据库：`ReplaceChunks` 幂等，`Search` 按距离排序（`DATABASE_URL` 存在时运行）。
- `httpapi`：参数校验，`503` / `504` 映射。
- 评测：`make eval-rag`。

## 6. 配置

| 变量 | 默认值 |
| --- | --- |
| `EMBEDDING_BASE_URL` | `http://127.0.0.1:8084` |
| `EMBEDDING_DIM` | `1024`（必须与迁移中的列维度一致） |
| `RAG_TOP_K` | `5` |
