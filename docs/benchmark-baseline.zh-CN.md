# 场景基线（2026-10-21 首轮 / 10-22 复测）

模型：`deepseek-v4.1-flash`（airoute 网关，`LLM_API_PROTOCOL=chat_completions`）。
语料：30 个场景。执行：`scripts/smoke_test.sh`，超时按 `task_length` 分配。

## 结果

首轮 **23 通过 / 7 失败（76.7%）**，全程 0 次超时。

修复后增量复测：**5/5 通过**，其中 `23_financial_ledger_large` 需要把
`MAX_MAIN_LOOP_ITERATIONS` 从 50 提到 80（见下）。据此预期基线为 **28/30（93.3%）**，
仅剩 17/18 两个"完成但未 finalize"的行为问题。

| 失败场景 | 现象 | 归类 |
|---|---|---|
| 15_unit_mismatch_explicit | 提问后收敛，未出报告 | 验收口径（已修） |
| 30_timeseries_multidim | 提问后收敛，未出报告 | 验收口径（已修） |
| 28_hr_crosstab | 420s 超时 | 超时预算（已修） |
| 23_financial_ledger_large | `模型服务调用失败` | 模型输出非法 JSON（**已修**） |
| 25_employee_wide | `模型服务调用失败` | 模型输出非法 JSON（**已修**） |
| 17_delegate_child_tool_failure_recovery | 完成但未 finalize | 模型行为 |
| 18_delegate_partial_recovery | 完成但未 finalize | 模型行为 |

### 已修：验收口径

15 与 30 是歧义场景，模型选择"提问"而不是"完成"。与 13/14 同类问题——歧义场景只认一条路径会把合法路径判成失败。已改为同时接受两种终态、配 `answers`、用 `resume_if_asked` 要求"若提问则必须走到完成"。

### 已修：超时预算

28 的 `task_length` 是 medium（420s），实测需要更多时间，改为 long（600s）。

### 已修：模型输出非法 JSON 会让整个 run 失败

23 与 25 的真实原因（服务端日志）：

```
invalid Chat Completions response: choice[0].tool_calls[1] arguments: duplicate JSON object key "grid"
invalid Chat Completions response: choice[0].tool_calls[0] arguments: duplicate JSON object key "block_id"
```

模型在工具参数里写出了**重复的 JSON key**。严格解码拒绝后，引擎把整轮 run 判为 `llm_request_failed` 并结束。

这是健壮性问题，不是场景问题：一次参数格式抖动就丢失整轮工作。契约严格本身没错，但这类"模型写出了语法上可修复的 JSON"应该按可恢复的工具失败反馈给模型重试，而不是终止 run。

修法：
- 响应校验只校验 id/name/type，不再因参数 JSON 非法而判整个响应失败——参数是模型输出，不是传输错误。
- 派发阶段校验参数，非法时返回 `error_code=invalid_tool_arguments` 的结构化失败，不执行该调用，模型可自行修正后重试。
- 同一类问题一并处理：模型编造不存在的工具名现在返回 `unknown_tool` 失败，而不是终止整轮。
- Google 路径重建请求时对非法参数退化为空对象，避免下一轮因历史里的坏参数再次失败。

### 复测后新暴露：50 轮上限对长场景不够

`23_financial_ledger_large` 在修复后不再死于解析，转而撞上 50 轮上限。日志显示模型反复用
`status=observed` 写工作记忆但没给 `source_result_ids`，被契约挡回后重试，消耗轮次。

把 `MAX_MAIN_LOOP_ITERATIONS` 提到 80 后该场景通过。

**但要注意方差**：止损机制实现后，把上限退回 50 再跑一次，23 同样通过，而该次运行里
`repeated_failure_blocked` 计数为 0——模型这次没有进入那个循环。单次运行不能证明
"上限必需"或"止损生效"，两个结论都只能由多次运行分布来定。这说明：
- 轮次上限做成可配是对的（这条改动正是为此）；
- 但模型反复违反同一个工具契约时，仅靠 repeat notice 提醒不足以止损，
  需要一个"同一错误码连续 N 次即终止或强制换路"的机制（尚未实现）。

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

---

# 可信基线（2026-10-22，--repeat 3）

30 个场景 × 3 次 = 90 次运行，模型 `deepseek-v4.1-flash`。

| 状态 | 数量 |
|---|---|
| `pass`（3/3） | **14** |
| `flaky`（1–2/3） | **11** |
| `fail`（0/3） | **5** |

全通过率 **46.7%**。对比单次基线的 76.7%——**单次基线高估了约 30 个百分点**。

## 关键：这批数据被两个「已修复」的缺陷污染

跑批用的是 09:37 构建的二进制，而两个修复是在它启动之后才提交的。失败原因分布直接反映了这一点：

| 失败原因 | 场景数 | 状态 |
|---|---|---|
| `运行时状态持久化失败` | 8 | **已修**（`fix(handler)` 非法参数编码占位） |
| `任务超过运行轮次上限（50）` | 3 | **已修**（默认改为不限） |
| 提问后收敛但验收只认完成 | 2 | **已修**（29 本次补，10 已补） |
| `report_finalized` 未达成 | 3 | **未修**，待定 |

所以 46.7% 不是当前代码的真实水平。**这批数据的正确用途是暴露缺陷类型，而不是给产品打分。**

## 仍然成立、且值得单独处理的信号

**1. 委托类场景系统性不 finalize（17/18 各 0/3，16 为 1/3）**
`terminal=run_completed` 但 `report_finalized` 从未达成。模型完成了分析却没有交付报告。这不是随机波动——3 次全一样。要么是模型对委派后的收尾有系统性缺陷，要么这两个场景的 `report_finalized: true` 要求本身不合理。需要产品口径判断。

**2. `financial_pl` 出现 `工具调用批次不符合协议`**
只出现过一次，属于模型输出协议违规，已被结构化拦下但令该轮失败。与 `duplicate JSON object key` 同类——模型在工具参数上不可靠。可考虑把批次级校验也降级为逐调用失败。

**3. flaky 占比 11/30 是最大的结构性问题**
即便把上面的缺陷全修掉，"通过"本身就是概率性的。任何单次红绿都不该作为发布判据。

## 结论

- **测量工具已经可信**（`--repeat` + flaky 判定），单次基线的结论不再采用。
- **下一批必须用当前二进制重跑**，否则数字没有意义。
- 本轮真正的产出是：把 7 类失败坍缩成 4 类，其中 3 类已修、1 类需要产品判断。
