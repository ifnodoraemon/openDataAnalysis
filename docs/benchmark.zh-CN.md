# 基准与回归评测

更新日期：2026-06-22

本文档定义本仓库的数据分析 agent 回归评测方式。目标不是复刻公开榜单，而是持续检查产品核心链路是否退化。

## 评测目标

覆盖这些能力：

- 数据导入：CSV、Excel、SQL source snapshot。
- 数据理解：schema、字段映射、时间粒度、单位、数据质量。
- 分析执行：只读 SQL、必要时 Python。
- 多表判断：join key、grain、口径冲突。
- 报告交付：图表、结论、限制说明、刷新恢复。
- Agent 行为：不过早强结论、不乱连表、失败后可恢复。

## 场景语料

主语料位于：

- `samples/coverage_scenarios/`
- `samples/coverage_scenarios/README.zh-CN.md`
- `samples/coverage_scenarios/CHECKLIST.generated.md`

每个场景目录包含：

- 一个或多个 CSV 文件。
- `scenario.yaml`：行业、任务、预期行为、必须出现/禁止出现的结论或工具结果。

生成人工验收 checklist：

```bash
python scripts/render_scenario_checklist.py \
  --output samples/coverage_scenarios/CHECKLIST.generated.md
```

## Runner

现有脚本：

- `scripts/run_scenario.js`：运行单个场景。
- `scripts/eval_report.js`：检查报告输出。
- `scripts/render_scenario_checklist.py`：渲染人工验收清单。
- `scripts/smoke_test.sh`：本地冒烟入口。

本地 Docker 环境启动后再跑场景，避免把服务启动问题和 agent 行为问题混在一起。

## 核心指标

第一层指标：

- `load_success`
- `schema_inspected`
- `sql_exec_success`
- `numeric_answer_accuracy`
- `chart_valid`
- `report_finalized`
- `report_reopen_success`

行为指标：

- `did_overclaim`
- `did_join_correctly`
- `did_state_limits`
- `did_ask_user_when_needed`
- `did_recover_from_tool_failure`
- `did_recover_from_delegate_failure`

数据源指标：

- `source_import_success`
- `semantic_profile_created`
- `ambiguity_surfaced`
- `confirmation_applied`
- `import_truncated_reported`

## 测试集选择

测试集按声明的能力覆盖选择，不能根据行业关键词或一组硬编码的优先场景 ID 选择。阻断测试集至少覆盖：单数据源成功分析、有效的多数据源关系、尚未解决的歧义，以及可恢复的失败。扩展测试集再覆盖不兼容的粒度、单位、关联键、委派失败和有界大数据导入。

## 触发条件

这些改动后应至少跑 blocking smoke：

- runtime prompt、tool schema 或 tool description 变更。
- `data_query_sql`、`code_run_python`、报告工具或 sanitizer 变更。
- 数据导入、semantic profile、confirmation 或数据源 API 变更。
- session/run/report 恢复逻辑变更。
- 上下文压缩、delegate 或 trace 逻辑变更。

## 判定原则

- 评测优先看行为是否可信，不只看报告是否“完整”。
- 证据不足时，partial answer 比强行完整报告更好。
- 生成图表必须基于已查询或已结构化保存的数据。
- sampled/truncated 数据必须在报告或限制说明中明确。
- 工具失败必须作为事实返回，由智能体选择另一条有效路径；运行时不能编码降级路线。
- 断言应优先检查结构化事件和状态。短语匹配仅用于展示检查，不能定义运行时语义。

## 语料完整性

每个包含 `.csv` 的场景目录都必须有 `scenario.yaml`。缺少清单的目录会被 runner 静默跳过，等于这部分语料从未被评测。`scripts/lib/scenario-yaml.test.js` 里有守卫用例：发现"有数据没清单"的目录即失败。

当前语料 30 个场景，覆盖单表、多表关联、粒度/单位/关联键歧义、委派失败恢复、宽表、层级、透视矩阵、队列留存、多维时序，以及最大 6.5 万行的有界导入。

## 歧义场景的验收口径

当场景本身是"口径不确定"的，**提问和显式假设都是合法路径**，验收不能只认其中一条：

- `terminal_types` 同时包含 `run_completed` 与 `user_request_input`。
- 配置 `answers`，让 runner 在模型提问后回传答案并继续（覆盖"问了之后能不能继续"这条链路）。
- 配置 `resume_if_asked: true`：若模型提问，则必须在回答后走到完成；若模型没提问，不判失败。
- 是否**显式说明**了口径与假设，由 `manual_review` 判断——这是内容质量，不适合用事件断言。

只认单一路径的验收会把另一条合法路径判成回归，噪声会盖住真实问题。

## 超时预算

场景的超时按 `task_length` 分配：`short` 300s、`medium` 420s、`long` 600s。场景可用 `timeout_seconds` 显式覆盖；`--timeout` 只在场景没有声明长度时兜底。

原因：300s 会让"长"场景在正常产出报告块的中途被截断（实测 19/24 在 300s 被截断、600s 全部通过），把正常工作判成失败。超时是评测参数，不是能力判据。

## 结构化失败

`failures_must_be_structured: true` 检查每一次 `tool_result` 失败都带机器可读的 `error_code`。

它替代了"要求某个工具必须失败"这类负向触发：那种用例依赖模型故意犯错才成立，模型一旦不配合就判失败，测的其实是运气。失败是否被当作事实处理，应该由通用契约保证，而不是写进特定场景的预期。

## 重复运行与不稳定场景

单次运行无法区分"确定性缺陷"和"模型方差"：同一个场景会在不同运行里通过和失败。`--repeat N` 把结果变成比率，并引入第三种状态：

| 状态 | 含义 |
|---|---|
| `pass` | N 次全部通过 |
| `flaky` | 有的通过有的失败 |
| `fail` | N 次全部失败 |

`flaky` 与 `fail` 一样让脚本以非零退出：它遇到一次运行就藏得住，到了生产才暴露。

首次用 `--repeat 2` 验证时，`07_retail_short` 就是 1/2——它在之前所有单次基线里都显示"通过"。

因此：**关键场景的结论必须来自多次运行**。单次红绿只能作为线索，不能作为判据；失败归因要能在多次运行里复现才算数。
