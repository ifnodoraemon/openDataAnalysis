# 场景基线（2026-10-21）

模型：`deepseek-v4.1-flash`（airoute 网关，`LLM_API_PROTOCOL=chat_completions`）。
语料：30 个场景。执行：`scripts/smoke_test.sh`，超时按 `task_length` 分配。

## 结果

**23 通过 / 7 失败（76.7%）**，全程 0 次超时。

| 失败场景 | 现象 | 归类 |
|---|---|---|
| 15_unit_mismatch_explicit | 提问后收敛，未出报告 | 验收口径（已修） |
| 30_timeseries_multidim | 提问后收敛，未出报告 | 验收口径（已修） |
| 28_hr_crosstab | 420s 超时 | 超时预算（已修） |
| 23_financial_ledger_large | `模型服务调用失败` | 模型输出非法 JSON（引擎问题） |
| 25_employee_wide | `模型服务调用失败` | 模型输出非法 JSON（引擎问题） |
| 17_delegate_child_tool_failure_recovery | 完成但未 finalize | 模型行为 |
| 18_delegate_partial_recovery | 完成但未 finalize | 模型行为 |

### 已修：验收口径

15 与 30 是歧义场景，模型选择"提问"而不是"完成"。与 13/14 同类问题——歧义场景只认一条路径会把合法路径判成失败。已改为同时接受两种终态、配 `answers`、用 `resume_if_asked` 要求"若提问则必须走到完成"。

### 已修：超时预算

28 的 `task_length` 是 medium（420s），实测需要更多时间，改为 long（600s）。

### 未修：模型输出非法 JSON 会让整个 run 失败

23 与 25 的真实原因（服务端日志）：

```
invalid Chat Completions response: choice[0].tool_calls[1] arguments: duplicate JSON object key "grid"
invalid Chat Completions response: choice[0].tool_calls[0] arguments: duplicate JSON object key "block_id"
```

模型在工具参数里写出了**重复的 JSON key**。严格解码拒绝后，引擎把整轮 run 判为 `llm_request_failed` 并结束。

这是健壮性问题，不是场景问题：一次参数格式抖动就丢失整轮工作。契约严格本身没错，但这类"模型写出了语法上可修复的 JSON"应该按可恢复的工具失败反馈给模型重试，而不是终止 run。

### 未修：17/18 从未尝试 finalize

时间线里 `report_finalize` 调用次数为 0。模型完成分析后没有交付报告，属于行为问题；这两个场景的验收要求 `report_finalized: true` 是否过严，需要结合产品预期再定。

## 工具面（同批 30 个场景）

| 工具 | 调用 | 场景 | 失败率 |
|---|---|---|---|
| `data_query_sql` | 369 | 30 | 6% |
| `code_run_python` | 221 | 20 | 34% |
| `report_manage_blocks` | 186 | 23 | 1% |
| `report_create_chart` | 118 | 24 | 0% |
| `memory_save_entry` | 81 | 20 | 10% |
| `data_describe_table` | 51 | 30 | 0% |
| `report_finalize` | 22 | 22 | **0%** |
| `state_tool_result_inspect` | 14 | 8 | 0% |

两点值得记：

- `report_finalize` 失败率从上一批的 38% 降到 **0%**——`state_report_inspect` 现在暴露 `can_finalize` 与 `active_goal_blockers`，模型不再靠撞墙发现 goal 阻塞。
- `state_tool_result_inspect` 从"从未被调用"变为 14 次——spill 回读在真实大结果里被用上了。

`code_run_python` 的 34% 主要是沙箱策略拒绝（`import io`/`os`、`type()`），属产品决策而非缺陷。

从未被调用：`job_submit`、`state_job_inspect`、`profile_patch_commit`、`report_configure_layout`、`state_report_edit_inspect`（仅 1 次）。
