# 覆盖测试场景

这组样例用于回归检查数据分析 agent 是否能在不同数据充分性、行业和任务跨度下给出可信分析。

## 关注点

- 先基于现有数据和 schema 判断可分析范围。
- 数据不足时明确边界，不编造缺失字段或指标。
- 多表分析时正确处理 join key、时间粒度和单位。
- 数据质量差时降低结论强度。
- 工具或子代理失败后能基于失败事实恢复或降级。

## 使用方式

每个场景目录包含：

- `scenario.yaml`：场景元数据、提问、预期行为。
- 一个或多个 CSV 文件。

生成完整人工验收清单：

```bash
python scripts/render_scenario_checklist.py \
  --output samples/coverage_scenarios/CHECKLIST.generated.md
```

运行单场景可使用：

```bash
node scripts/run_scenario.js samples/coverage_scenarios/01_sales_complete/scenario.yaml
```

## 场景分组

### 字段充分性

- `01_sales_complete`：单表字段充分。
- `02_sales_missing_region`：缺关键维度，但可部分分析。
- `03_marketing_spend_only`：缺收入，不能计算 ROI。
- `04_roi_joinable`：多表可按 `month + channel` 关联。
- `05_roi_unjoinable`：多表缺稳定关联键。
- `06_sales_quality_gaps`：存在明显数据质量问题。

### 任务跨度与行业

- `07_retail_short`：零售短任务。
- `08_saas_medium`：SaaS 中等任务。
- `09_manufacturing_long`：制造业长任务。

### 真实边界

- `10_retail_alias_headers`：业务别名和缩写字段。
- `11_mixed_language_headers`：中英混合字段。
- `12_ambiguous_metrics`：多个收入口径。
- `13_join_key_conflict`：表面可连但粒度或主键冲突。
- `14_time_grain_reconcilable`：日/月粒度可先聚合再分析。
- `15_unit_mismatch_explicit`：字段名显式表达单位差异。

### 失败恢复

- `16_delegate_failure_recovery`：delegate 创建失败。
- `17_delegate_child_tool_failure_recovery`：子代理内部工具失败。
- `18_delegate_partial_recovery`：子代理只返回局部不足结论。

### 大数据

- `22_daily_retail_large`
- `23_financial_ledger_large`
- `24_server_metrics_large`

## 判定维度

建议每次记录：

- 是否 inspect schema。
- 是否正确自动映射字段。
- 是否在真实歧义时询问用户。
- 是否避免证据不足时强结论。
- 是否正确处理 join、grain、unit。
- 是否明确限制、缺口和假设。
- 是否完成最终报告和图表。
- 是否从 delegate/tool 失败中恢复。
