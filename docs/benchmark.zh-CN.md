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
