# P3 投机解码延迟实验报告

- 日期：2026-10-29
- 关联：[P3-1 报告](2026-10-23-p3-1-insights-report.md) §3.3、[P3 需求](../requirements/2026-10-23-p3-agent-pipeline-requirements.md) E5
- 用户已确认（2026-10-29）：先试投机解码，不达标再记录为已知缺口继续推进；门槛 8 秒不降低。

## 1. 环境与方法

- 同一台 Apple M1 Pro；主模型 Qwen2.5-7B-Instruct Q4_K_M，新增草稿模型 Qwen2.5-0.5B-Instruct Q4_K_M（sha256 `6eb923e7…`，已写入 `server/scripts/download-llm-models.sh`）。
- 启动：`make run-llm-speculative`（`-md <draft> --spec-type draft-simple --spec-draft-n-max 3`），其余参数与基线一致（`-c 8192 -np 1`）。
- 评测：`go -C server run ./cmd/insighteval -cases eval/p3/cases.json`，20 条文字模式，与基线同一评测集与口径。
- 原始数据：`output/p3-1-speculative-eval.jsonl`、`output/p3-1-speculative-summary.json`、`output/p3-llm-speculative.log`。
- **混杂因素**：基线测量时系统负载 100～190，本次约 12；延迟改善不能全部归因于投机解码，结论标为“待同负载复测”。

## 2. 数据

| 指标 | 基线（无投机，2026-10-23） | 投机解码（2026-10-29） |
| --- | --- | --- |
| 要点召回率 | 88.6%（93/105） | **90.5%**（95/105；温度 0 下的运行间波动） |
| 格式失败（原始 / 最终） | 0 / 0 | 0 / 0 |
| 延迟 P50 | 12.5 s | 11.4 s |
| 延迟 P95 | 15.5 s | **13.1 s** |
| 草稿接受率 | — | 71%～77%（`draft acceptance = 0.71～0.77`，mean len ≈ 3.2） |
| 生成速度（server 自报） | 约 23～24 token/s | 约 23.8 token/s（末条样本） |

## 3. 结论

1. **E5 仍未达标**：P95 13.1 s > 8 s 门槛。有趣的是草稿接受率高达约 73%，但 llama-server 自报的逐 token 速度几乎没变——推测（待验证）：草稿模型同在 Metal 上运行、与主模型争抢带宽，接受收益被草稿前向开销抵消；也可能是当前 llama.cpp 构建对 draft-simple 的批处理实现使然。
2. 按用户决策，本缺口**记录保留、不再阻塞**：纪要为异步任务，13 秒级延迟不影响主链路；问答（R6）输出短，缺口更小。
3. 后续可选（未实施）：换 3B 主模型、压缩输出 schema、或等 llama.cpp 投机实现更新后同负载复测。本报告数字因负载差异标为待复测。

## 4. 附带完成项

- `make eval-rag` 入口补齐（此前 P3-2 方案提到但 Makefile 缺失）。
- P3-2 评测已补记为 [P3-2 报告](2026-10-29-p3-2-rag-report.md)。
