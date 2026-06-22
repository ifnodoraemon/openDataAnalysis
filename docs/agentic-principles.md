# Agentic Principles

更新日期：2026-06-22

本文档定义本项目的 agent runtime 边界。详细、持久、面向工具实现的规则放在 `AGENTS.md`；这里保留设计原则和取舍。

## 目标

后端是 agent runtime，不是隐藏 workflow engine。

Runtime 负责：

- 暴露目标、工具、状态和薄 guardrail。
- 管理上下文、并发、取消、持久化和 trace。
- 校验最终产物结构是否可交付。

模型负责：

- 判断下一步行动。
- 决定是否探索、提问、委派、写报告或收尾。
- 在不确定时基于事实向用户确认或显式说明假设。

## 核心原则

### 1. 不预设路径

系统可以定义目标和边界，但不能把 `analyze -> write -> finalize` 这类固定步骤编码进 prompt、工具描述或 handler 拼接消息。

### 2. 状态只暴露事实

`state_*` 工具返回当前世界状态，例如数据源、语义画像、工作记忆、目标树、报告结构和时间上下文。它们不能返回“下一步应该做什么”。

### 3. Judge 属于模型

Runtime 不替模型判断：

- 是否应该 delegate
- 证据是否足以写结论
- 哪个章节应该先补
- 何时应该追问用户

这些判断应由模型读取事实后自行完成。

### 4. 工具契约详细但不指路

工具 description 可以说明用途、输入、输出、副作用、限制和失败条件，但不能暗含 workflow，也不能返回 `next_action`、建议调用某工具等字段。

展示摘要使用 `ui_summary`；模型和工具链需要的事实放在结构化字段中。不要新增 `summary_text` 这类语义混杂字段。

### 5. Guardrail 只拦坏结果

允许的硬约束：

- 阻止非法 SQL、危险执行或损坏状态。
- 阻止引用不存在的 chart/block/source。
- 阻止结构非法的 report finalize。
- 阻止报告预览绕过 sanitizer、CSP 和可信脚本边界。

Guardrail 不应该规划正常路径。

### 6. Finalize 是交付边界

`report_finalize` 成功返回 `ok=true`、`is_finalized=true`、`delivery_state=finalized` 后，当前 run 应进入收尾。Runtime 可以给模型一次 tools-disabled 的自然续写机会，再结束 run；不要用硬编码模板替代模型最终回复。

### 7. 歧义不能静默拍板

当分析依赖多个合理口径时，系统应暴露候选事实，由模型判断是否需要用户确认。典型歧义包括：

- 多个收入/成本/利润口径。
- 多个 join key。
- 日、周、月粒度混用。
- 元、万元、美元、百分比等单位冲突。
- 字段名别名存在多个高相似候选。

用户明确允许自行假设时，模型可以继续，但必须说明假设。

### 8. 状态默认 pull-based

不要每轮自动注入大段文件摘要、报告结构、工作记忆或历史 digest。需要状态时，模型应通过 observation tool 拉取。

### 9. 结构化状态是脚手架

Working memory、goal tree、report block tree 可以提升可观察性和恢复能力，但不能变成强制 planning phase 或固定报告章节模板。

### 10. 报告脚本和模型内容分层

模型负责报告内容和结构化 chart option 数据。ECharts loader 与 chart runtime 使用可信外部脚本，优先同源 `/assets/echarts.min.js` 和 `/oda-chart-runtime.js`。不要为图表渲染放开宽泛 inline script。

## Prompt Layering

本项目采用四层上下文：

1. `system`：静态 policy 和工具边界。
2. `user`：用户直接任务。
3. `runtime`：当前轮客观事实，例如编辑范围或活跃目标。
4. `history`：对话历史、工具结果和压缩摘要。

临时事实、历史摘要和用户偏好不能提升到 `system` 层。子代理额外约束使用 `policy_appendix`，不能用它倾倒背景事实。

## 当前反模式

- 线性 workflow prompt。
- 工具结果返回下一步建议。
- handler 在用户消息里拼“请先调用某工具”。
- runtime 把低置信候选包装成 verified fact。
- 因为模型偶尔做错，就把判断逻辑搬进代码。
- 为稳定性每轮自动注入大量状态。
