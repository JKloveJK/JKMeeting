# P3-3 MCP Server 技术方案

- 日期：2026-10-29
- 状态：已实施（stdio），待用户在 Cursor 重启后确认真实加载
- 需求：[P3 Agent 管道需求 · P3-3](../requirements/2026-10-23-p3-agent-pipeline-requirements.md)
- 用户已确认（2026-10-29）：stdio 传输，以 Cursor / 本机 MCP 客户端真实加载验证；不引入 MCP 官方 SDK 大型依赖，手写最小 JSON-RPC 协议层（标准库实现，协议面可单测）。

## 1. 设计

| 问题 | 选择 | 依据 |
| --- | --- | --- |
| 传输 | stdio，newline-delimited JSON-RPC 2.0 | 用户确认；零端口暴露，凭据不经网络 |
| 协议 | `initialize` / `ping` / `tools/list` / `tools/call`，协议版本回显客户端版本 | MCP 最小可用面；通知不回响应 |
| 日志 | 全部走 stderr，stdout 只写协议消息 | stdio 协议硬约束，混入日志即损坏协议 |
| 工具级错误 | 返回 `isError=true` 的 text content，协议错误才用 JSON-RPC error | 与 MCP 约定一致，客户端可把失败原因展示给模型 |
| 依赖面 | `internal/mcp.Service` 接口（5 个方法），业务实现复用 `rag.Repository`、`insights.Repository`、`embedding.Client`、`rag.Answerer` | 与 HTTP API 共用同一业务层，不复制检索/问答逻辑 |

## 2. 工具集

| 工具 | 参数 | 语义 |
| --- | --- | --- |
| `list_recordings` | `limit`（1～50，默认 10） | 最近录音列表，先定位会议 |
| `get_transcript` | `recording_id` | 转写状态与全文 |
| `get_insights` | `recording_id` | 纪要状态与结构化结果；无记录返回 `status=none`（与 HTTP 语义一致） |
| `search_meetings` | `query`、`top_k`（1～10） | 纯向量检索片段，不调 LLM |
| `ask_meetings` | `question`、`top_k` | RAG 问答，引用校验与拒答语义与 `POST /v1/ask` 相同 |

## 3. 文件

- `server/internal/mcp/server.go`：协议层（与业务无关，可 fake 单测）。
- `server/internal/mcp/service.go`：业务实现（pgx + rag + insights）。
- `server/cmd/mcp/main.go`：入口，仅要求 `DATABASE_URL`，其余沿用 `EMBEDDING_BASE_URL` / `LLM_BASE_URL` / `LLM_MODEL` 默认值。
- `server/scripts/test-mcp.py` + `make test-mcp`：真实子进程冒烟（initialize → tools/list → tools/call）。
- `.cursor/mcp.json`：工作区级 Cursor 加载配置；命令从 `.env`（git 忽略）注入 `DATABASE_URL`，凭据不进仓库。

## 4. 验证

- `go test ./internal/mcp/`：协议单测（initialize、tools/list、tools/call、未知工具、缺参）。
- `make test-mcp`：连真实数据库的端到端协议冒烟。
- 真实客户端加载：用户重启 Cursor 后在本会话确认 `jkmeeting` 工具可见（配置已就位，属人工确认项）。
