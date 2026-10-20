# DSH 对照评审：本项目的改进清单

评审基线：`639ab6f`（2026-09-26）。对照对象：DeepSeek Harness（DSH）`@deepseek-ai/dsh` 0.2.0-rc.2 的包设计文档，以及公开仓库 [deepseek-ai/deepseek-harness](https://github.com/deepseek-ai/deepseek-harness) 与 [文档站](https://deepseek-harness.github.io/deepseek-harness/)。

本文只讨论**运行时工程**，不讨论产品定位。项目自身的产品原则（`docs/product-first-principles.zh-CN.md`）和 agent 原则（`docs/agentic-principles.md`）已经相当克制且自洽，多数方向与 DSH 一致；下面列的是 DSH 走得比我们更远、或我们明显存在缺口的地方。

> **实施状态（2026-10-20）**：P0 两项已落地。P0-1 由 `server/agent/history_rebuild.go` 的 `RebuildHistoryFromMessages` 实现，并在 `hydrateSessionFromPersistence` 中接入；P0-2 由 `server/agent/compaction.go`、`server/agent/tokens.go` 实现，新增 `LLM_CONTEXT_WINDOW_TOKENS` 配置与 `history_compacted` checkpoint 事件。详见第 3 节各项末尾的"已实现"说明。
>
> P1 部分落地：重试幂等化、异常退出的悬挂工具调用修复、`continuationFacts` 清理、子代理上下文窗口去重与结果归一化（commit `9b77c83`），MCP 工具按注解声明幂等性（commit `6060e5f`），以及**权限与审批收敛（P1-3）**：`tools.DecideToolCall` 成为唯一准入判定点（allow/ask/deny），所有失败路径 fail closed，每次授权请求、用户决定与拦截都写入 `audit_events`，receipt 增加 30 分钟有效期，准入规则写入静态 policy。
>
> P1 全部落地：重复调用提醒（3/5/8 次，作为 runtime 事实下发而非替换工具结果）、可用性探测去阻塞（500ms 上限，探测无结论时保留工具而非隐藏它）、以及有界并行工具执行（显式 opt-in，exclusive 调用仍是顺序屏障，结果与事件保持模型顺序）。**P1 至此完成。**
>
> 并行执行的落地范围经过证据约束：本地数据工具的会话分析库以 `SetMaxOpenConns(1)` 打开，重叠它们只会增加争用而没有真实并发，因此**不 opt in**；唯一 opt in 的是声明了 `readOnlyHint` 的 MCP 工具——它们是对独立外部端点的 HTTP 往返，是真正能受益的场景。

---

## 1. 总体结论

我们的 agent 循环、提示分层、工具契约和 SSE 回放**方向是对的**，主要缺口集中在三处：

1. **模型上下文没有持久化来源**。会话历史只存在于进程内存，重启或 TTL 淘汰即失忆；`run_messages` 里其实已经存了足够的原始事件，但没有任何代码把它还原成模型历史。
2. **上下文预算与压缩是"事后丢弃"**。预算硬编码 128k、触发依赖 provider 回报的 usage、裁剪按消息条数、且被裁掉的内容永久丢失，只留一个 `omitted_message_count`。
3. **工具执行管线缺少治理层**。串行执行、无按工具超时、无统一结果裁剪/落盘、重试不分幂等性、失败时工具调用与结果可能不成对。

其余（权限统一、schema 单一来源、事件编解码去重、token 计量、可观测性）属于工程一致性改进，优先级低于上面三条。

---

## 2. 我们做对了、应当保留的部分

对照时容易只看到差距，以下设计**不要**因为对照 DSH 而改掉：

| 我们的设计 | 位置 | 为什么保留 |
|---|---|---|
| 严格 JSON 工具结果契约（`ok` + `tool`，失败带 `error_code`/`message`） | `server/agent/engine.go:766-795`、`server/tools/response.go:13-31` | 比 DSH 的 `Error: <message>` 归一化信息量更大，模型和 UI 都能结构化消费 |
| `ui_summary` 与事实字段分离，进历史前剥离 | `server/agent/engine.go:740-764` | 与 `AGENTS.md` 一致，DSH 也没有更好的替代 |
| finalize 交付边界 + 一次禁用工具的续答 | `server/agent/engine.go:588-596`、`engine.go:648-714` | DSH 没有对应机制，这是报告类产品的必要边界 |
| SSE `Last-Event-ID` 游标回放 + overflow 信号 | `server/handler/sse.go:132-135`、`sse.go:217-257`、`sse.go:325` | 机制正确，问题只在细节（见 P2-1） |
| 拉取式 `state_*` 观察工具，不自动注入大状态 | `server/agent/state_tools.go`、`server/tools/source_tools.go` | 与 DSH 的 projection/按需读取同构 |
| 轮次上限 `maxMainLoopIterations = 50` | `server/agent/engine.go:55` | DSH **明确没有**内置轮次预算，把它留给策略插件；我们有硬上限反而更安全（但应可配置，见 P1-4） |

---

## 3. P0：模型上下文可恢复性

### P0-1 会话历史只存在于内存

**现状**

- `Engine.history` 是纯内存切片（`server/agent/engine.go:23-32`），没有任何持久化或重建路径。
- `hydrateSessionFromPersistence` 只从事件里还原 working memory、subgoals、report snapshot 和 edit state（`server/handler/session_runtime.go:46-68`、`server/handler/runtime_state.go:143-199`），**不还原对话历史**。
- 因此：进程重启后 `recoverStaleRunTree` 直接把 running/waiting 的 run 标记为失败（`server/handler/session_runtime.go:90-98`）；等待用户输入的任务无法跨重启恢复。
- 讽刺的是原始素材是齐的：`run_messages` 逐条持久化了 user、assistant_status、tool_call、tool_result 等事件（`server/handler/runtime_events.go:492-568`），只是没有读者。

**DSH 的做法**

- 会话日志是**唯一事实来源**，`deriveMessages()` 从日志投影出模型历史（`dsh-session`）。持久化是独立 seam：`append` 是尽力而为、`flush()` 才是持久化屏障（`dsh-session-persistence`）。
- 崩溃恢复有明确的**收尾事件**：未确认的请求记 `TOOL_NOT_STARTED`，有调用无结果记 `TOOL_OUTCOME_UNKNOWN`，再补一个 `turn/end {interrupted}`，保证派生出的历史永远满足 tool-call/result 配对协议。
- 表面（surface）事件用 `surfaceOp = append | {op:'replace', startSeq, endSeq}` 表达"替换而不删除"，原始事件永久保留，因此回放是确定性的。

**建议**

1. 新增 `deriveModelHistory(sessionID) ([]ConversationItem, error)`：按 run 顺序折叠 `run_messages`，还原 user / assistant / tool_call / tool_result 序列。
2. 在 `hydrateSessionFromPersistence` 中调用它填充 `Engine.history`，让重启后的会话可以继续对话。
3. 加载时执行**配对修复**：为没有结果的 `tool_call` 合成一条 `{"ok":false,"tool":...,"error_code":"tool_outcome_unknown"}` 结果，为没有调用记录的挂起请求合成终止事件。没有这一步，派生历史可能违反 provider 的工具消息协议（Google 路径尤其敏感，见 `server/agent/llm_google.go:128-142`）。
4. 给 `run_messages` 增加 `surface_op` 与 `source_event_ids` 两列，为后续压缩/编辑提供"遮蔽而非删除"的表达能力。

**影响**：这是唯一一条"不做就会持续丢用户工作"的改进。工作量中等（折叠逻辑 + 修复 + 测试），但收益最大。

**已实现**：`server/agent/history_rebuild.go` 提供 `RebuildHistoryFromMessages`（纯函数，按事件类型折叠，含工具配对修复与"结果未知"合成），`Engine.SetHistory`/`History` 提供注入与读取，`hydrateSessionFromPersistence` 在会话冷加载时调用。同时让 `user_request_input` 挂起事件携带真实 tool call id（`AskUserData.ToolCallID`），使日志对挂起/恢复链路自描述。

---

### P0-2 上下文预算与压缩策略

**现状**

- 预算硬编码：`contextBudgetTokens = 128000`、触发线 `115200`、保留窗口 `recentContextWindow = 12`（`server/agent/engine.go:51-56`）。与真实模型窗口无关。
- 触发时机**晚一轮**：压缩用 `resp.Usage.PromptTokens`（`server/agent/engine.go:350`、`engine.go:711`），即"已经发出去的那次请求"的用量，超限请求已经发生。
- **usage 缺失就永不压缩**：`if promptTokens <= 0 { return }`（`engine.go:117`、`engine.go:177`）。任何不回报 usage 的网关都会让上下文无限增长直到报错。
- 裁剪是**按条数**（保留最后 12 条），不是按 token。
- 被裁掉的内容**永久丢失**，模型只看到 `{"omitted_message_count":N}`（`engine.go:328-332`）。
- 原始任务会被裁掉：任务先进入 `history[0]`（`engine.go:342-349`），随后可能被前向裁剪丢弃。

**DSH 的做法**

- `dsh-token-meter` 提供确定性估算：固定 4 字符/token 启发式 + 块与角色开销，仅在"规范请求信封完全一致"时复用 provider usage；每个模型可配策略。
- 阈值与保留比：`thresholdRatio 0.8`、`retainRatio 0.16`、`headroomTokens 65536`，触发条件 `floor(min(W × ratio, W − O − headroom))`。
- 压缩把最老的**平衡区间**总结成一条 `user/message`，带 `COMPACT_CHECKPOINT_MARKER`；原始事件保留在日志里，回放能确定性复现同一份压缩后对话。
- 溢出恢复：provider 确认 `CONTEXT_WINDOW_EXCEEDED` 时绕过阈值直接压缩并重试。
- 另有 `dsh-compaction-tool-result-pruner`（8192 字符阈值、4096 头 + 1024 尾，按 Unicode 码点计数，插入显式 `[... tool result middle pruned ...]` 标记）。

**建议**

1. 实现 token 估算回退：`chars/4 + 每条消息固定开销`，在 provider usage 缺失或为 0 时使用；永远不要因为 usage 为 0 而跳过压缩。
2. 把窗口大小改为**按模型配置**，而不是常量 128000。
3. 把压力检查前移到**发请求之前**（测量 + 压缩 + 再发送），而不是用上一次响应的事后用量。
4. 把"丢弃"换成"总结后替换"：压缩产生一条带 checkpoint 标记的持久事件，原始事件保留。注意这与 `docs/agentic-principles.md` 第 8 条并不冲突——那条禁止的是 **runtime 自己编造语义摘要**；由模型（或一次专门的 compaction 调用）撰写、并作为事实写入日志的摘要，正是 `docs/building-agent-lessons.zh-CN.md` 第 5 条要求的。
5. 增加工具结果裁剪器：头/尾窗口 + 显式省略标记 + 省略字节数。
6. 保证原始任务不可被裁剪（放到 policy 或 runtime 层，或标记为 pinned）。

**影响**：直接决定长任务成功率。当前实现下，长分析会在"压缩晚一轮 + 丢信息"的组合里逐渐漂移。

**已实现**：`server/agent/tokens.go` 提供 token 估算（ASCII 4 字符/token，非 ASCII 1 token/rune，避免 DSH 文档承认的 CJK 低估问题）；`server/agent/compaction.go` 在**发请求前**做压力检查，超阈值时调用模型生成摘要并替换最旧区间，写出 `history_compacted` checkpoint；摘要失败则退化为 trim 并在 checkpoint 中标记 `method=trim`；`pruneToolResultForHistory` 对超大工具结果做头尾裁剪并保留 `ok`/`tool` 契约。`LLM_CONTEXT_WINDOW_TOKENS` 取代硬编码的 128000。重建逻辑按 checkpoint 的 `shadowed_items` 复现压缩后的历史，计数不可用时宁可多留上下文也不误删。

---

## 4. P1：健壮性

### P1-1 工具执行管线

| 问题 | 现状 | DSH 做法 | 建议 |
|---|---|---|---|
| 串行执行 | 工具调用在一个普通 `for` 循环里逐个 await（`server/agent/engine.go:437`） | `isConcurrencySafe(args)` 纯函数分类，仅显式 `true` 并行；`maxParallelToolCalls` 默认 10；exclusive 调用构成顺序屏障 | 给 `ToolCapability` 增加 `ParallelSafe`，用有界并发池执行并**保持结果顺序** |
| 无按工具超时 | 只有 ctx 取消，没有单次调用预算 | `timeoutMs` 声明式，由 `dsh-tool-call-timeout-policy` 包装 `tools/execute` 强制执行，超时返回 `TOOL_TIMEOUT` | 增加超时包装层，超时作为正常工具失败返回给模型而不是杀 run |
| 可用性探测阻塞 | `filterUnavailableToolSpecs` 在每次 Run 开始时做实时 HTTP 探测（`engine.go:233-251`、`engine.go:287-290`），注释自述单次可达 5–6 秒 | 可用性声明式，工具保持可见、调用时返回结构化错误（如 `WEB_PROVIDER_UNAVAILABLE`） | 改为带 TTL 的缓存探测或声明式可用性；不要在 Run 启动路径上做同步网络探测 |
| 探测与派发不一致 | 只过滤了"广告出去的 schema"，派发时 `registry.Get` 不再校验可用性（`engine.go:438`） | 派发前策略/守卫统一裁决 | 派发时复用同一可用性判定 |
| 无重复调用提醒 | 无 | `dsh-repeat-tool-reminder` 在 3/5/8 次相同调用时给出建议性提示，不阻塞 | 增加建议性提醒（我们的 50 轮上限之外多一层软保护） |

### P1-2 工具结果治理

**现状**：进入历史的工具结果没有统一大小上限，截断散落在各工具内部与 delegate 路径（`server/agent/worker.go:821` `clipDelegateToolResult`）。`ui_summary` 只在主循环被剥离，worker 不剥离（`worker.go:570-575`）。

**DSH 做法**：`dsh-output-retention` 提供 `ItemRetainer`/`TextRetainer`（head / tail / headTail，按字节，不切断码点，返回精确 `omitted` 计数）；`dsh-spill` 把超大结果存到 store 并返回不透明 locator，`dsh-spill-policy` 按 `maxInlineTokens` 决定内联预览还是落盘。

**建议**：抽出统一的 `retainText` 工具函数（头/尾窗口 + 省略计数），在所有工具结果进入历史前统一应用；为超大结果增加落盘 + locator 机制；让 worker 与主循环走同一条结果规范化路径（当前是重复实现）。

### P1-3 权限与审批统一

**现状**：`RequiresUserReceipt`、`user_request_input` 挂起、authorization action/resourceRef、confirmation receipts（`server/agent/engine.go:36-49`、`engine.go:798-960`）是几套并行的机制；receipt 只存在内存 map 里、无过期、重启即失效（`engine.go:29`）。

**DSH 做法**：`dsh-tools` 的 `PreToolDecision = allow | deny | cancel | ask` 是唯一的准入裁决点；`dsh-user-approval` 的结果枚举 `allowed-once | rejected | cancelled | unavailable`，**缺失应答者时 fail closed**；每次 ask/decide 都写审计事件；`dsh-permission-presets` 把沙箱模式与审批策略打包成命名预设；有效策略作为 runtime context 事实注入给模型。

**建议**：

1. 把准入收敛到单一判定点（allow/deny/ask），让现有 capability 标志成为它的输入而不是各自为政的分支。
2. **fail closed**：无法取得应答者时拒绝，而不是放行或挂起。
3. 复用已有 `audit_events` 表记录每次 ask/decide。
4. receipt 增加过期与持久化，避免重启后无法继续已确认的操作。
5. 把当前有效策略作为 runtime 事实暴露（我们现在的 policy 是纯静态字符串，`server/agent/prompts.go:7-13`）。

### P1-4 重试与失败配对

**现状**

- `retryableToolExec` 对任何瞬时传输错误重试 1s/2s/4s（`server/agent/engine.go:88-110`），**不区分工具是否幂等**——`data_import_artifact`、`code_run_python`、`report_manage_blocks`、`profile_patch_commit` 都可能被重复执行。
- 工具结果 JSON 解析失败会**直接终止整个 run**（`engine.go:575-579`），而不是把契约错误作为一次工具失败反馈给模型。
- 取消/跳过时未为已发出的 `tool_call` 补结果，历史可能不成对。
- `continuationFacts` 一旦被赋值就**每轮重复注入**（`engine.go:302` 声明、`engine.go:371` 赋值、`engine.go:327` 每轮追加），运行后期会一直携带过期的 `report_delivery_state`。
- worker 的 `compactWorkerBundle` 每轮**追加**新的 `history_window` 块而不是重建（`engine.go:128-131`），会累积重复块。

**DSH 做法**：失败步骤明确记 `TOOL_OUTCOME_UNKNOWN` / `TOOL_NOT_STARTED`，保证后续请求的工具历史始终配对，且**不自动重试**；重试只在 LLM 请求边界（`dsh-llm-retry`）而非工具执行边界。

**建议**：给工具增加幂等性声明，只对只读工具做传输层重试；把结果契约错误降级为一次普通工具失败；在任何提前返回路径上补齐工具结果；把 `continuationFacts` 改为"消费后清空"；worker 改为重建 runtime context。

---

## 5. P2：工程一致性

### P2-1 事件与流式协议

| 问题 | 位置 | 建议 |
|---|---|---|
| SSE 事件全部匿名，客户端只能绑 `onmessage`；`overflow` 是命名事件却没人监听 | `server/handler/sse.go:310-318`、`sse.go:325` vs `client/src/composables/useAgentTransport.js:135` | 给事件加 `event:` 名（或统一按 `type` 字段分发），并绑定 overflow |
| 未知游标导致**重放全部消息** | `server/handler/sse.go:251-256` | 未知游标应回退到显式"全量重同步"信号，而不是静默重放整段历史 |
| 回放按 run 硬截断 5000 条且静默 | `server/repository/sqlite/message_repo.go:27,75` | 加分页，或至少返回"被截断"事实 |
| 三处重复的事件编解码 | `runtime_events.go:506` / `sse.go:259` / `runtimeEventDispatch.js:78` | 收敛到单一 schema（后端生成 + 前端消费） |
| 无部分输出流 | `llm.go:796` 固定 `Stream: false` | 若要首字延迟体验，需引入 assistant attempt/stream 语义（DSH 的 `start/chunk/end` + `committed/abandoned`） |
| ID 熵只有 32 位 | `session/types.go:97,320`、`runtime_events.go:144` | 提高熵，避免碰撞与枚举 |
| `run_started` 等非持久化事件永不回放 | `chat.go:175` | 明确区分"可回放"与"仅实时"事件集合并在文档中固定 |

DSH 在这块的整体做法是：**可恢复游标 + 快照基线 + 只对持久事件做 gap-free 回放**，部分输出走独立的进程内流。我们已有游标骨架，补细节即可。

### P2-2 持久化与 schema

- **两份 schema 定义**：Postgres 走 goose（`server/migrations/*.sql`），SQLite 走 `metadata/store.go:62-323` 内联 `CREATE TABLE IF NOT EXISTS` + PRAGMA 列校验（`store.go:325-347,380-410`），且 SQLite **没有版本表**。漂移只能靠临时列检查发现。
- **报告 ID 固定**：`rep_<runID>` / `report_<runID>`（`server/service/file_service.go:261,307`）对 `reports.run_id UNIQUE`（`migrations/001_init.sql:96`），同一 run 重复 finalize 会冲突而不是 upsert。
- **会话删除绕过 repository**：手写 SQL 重复了 ON DELETE CASCADE（`server/handler/session_delete.go:64-80`）。

DSH 的对应做法是构建期静态、无缺口、不可运行时改动的格式迁移链（v0→v4），每个版本变更都留变更记录与类型指纹。我们不需要那么重，但**至少要有一个版本表和单一 schema 来源**。

建议：以 goose 为唯一来源（goose 支持 SQLite），删除内联 DDL；加 schema 版本表；为 SQLite/Postgres 加一致性测试。

### P2-3 可观测性与成本

- 根 run 的 token 用量**只写进 debug trace**，且仅在 `LLMDebug` 打开时（`llm.go:338-340,730-732`）；持久化的只有 delegate 的 `child_run_tokens`（`runtime_events.go:214-222`）。没有成本可见性，也没有"这次分析花了多少"的产品事实。
- DSH 有 `dsh-session-stats`（turns/steps/llmMs/toolMs/ttft/decode）、`dsh-session-telemetry`（带脱敏 waterfall 的采集 seam）、以及 `dsh-invariants`（包自带的运行时自检）。

建议：至少持久化每次 run 的 prompt/completion/cache token 与耗时；补一个 `state_run_stats_inspect` 之类的观察工具；考虑用不变量自检来守护 `run_messages ↔ 派生状态` 的一致性（这正是 P0-1 落地后最容易出错的地方）。

### P2-4 单进程假设

`IsSessionLive` 是进程内 map 查询（`session/manager.go:277-282`），`runBackend` 只有 `inprocess` 一个实现且生产模式直接拒绝（`config/config.go:322-326`）。当前架构下多实例会互相把对方的活跃 run 判为失败。这不是 bug，但应在 `docs/maas-production-architecture.md` 里与 P0-1 一起排期——因为"历史可重建"是"run 可跨进程恢复"的前置条件。

---

## 6. P3：能力扩展（按需，不急于做）

| 能力 | DSH 形态 | 我们的现状 | 备注 |
|---|---|---|---|
| 后台任务 | `dsh-jobs`：稳定 `<kind>-N` id、owner fencing、环形输出缓冲 + spill、结算通知 | 无；长 SQL/Python 阻塞在工具调用里 | 对"跑一个大查询"体验提升明显 |
| 子代理 | `spawn`（全新上下文）vs `fork`（复制到最后一个 `turn/end`）、continuable、`list_agents`/`send_message`/`interrupt_agent`、`maxActiveSubagents` 默认 8 | 只有 `task_delegate`（全新上下文、一次性） | 我们已有 delegate 骨架；`fork` 语义值得补 |
| Goal 持久化 | `goal/change` 事件 + `{id, revision}` CAS + 默认 256 轮 + `blockedAfterConsecutiveRounds` 默认 3 | goal tree 在内存，随会话丢失 | 与 P0-1 同源问题 |
| 技能/命令 | `dsh-skill` + `dsh-commands`，文件系统多根发现、按需加载、`/` 命令 | 无 | 对"可复用分析套路"有价值 |
| 插件/扩展 | 一切皆插件（289 个包），profile/bundle/patch 分层 | 单体 Go + 已有 MCP 目录 | **不建议照搬**，见下节 |

---

## 7. 不建议照搬的部分

1. **289 包插件化架构**。DSH 自己的 Cordis 文档承认 API 不稳定（`@deepseek-ai/cordis` 0.4.0.5，官方文档"仍在建设中"），且它明确标注为技术预览、会有破坏性变更。我们的单体 Go 服务 + MCP 外部工具已经覆盖了主要扩展需求；把它拆成插件树是纯粹的复杂度支出。
2. **没有轮次预算**。DSH 把"限制失控轮次"交给策略插件（`agent/turn-stopping`）。我们有 `maxMainLoopIterations = 50` 是优点，只需把它变成可配置项，不要为了"对齐 DSH"而删掉。
3. **`Error: <message>` 的结果归一化**。DSH 把结构化错误码只留给调用方、对模型只暴露一行文本。我们的 `ok`/`tool`/`error_code`/`message` 契约对模型更友好，保留。
4. **`dsh-fs-observation-policy` 的 read-before-write**。它是为文件编辑工具设计的；我们的工具面是数据导入、SQL、报告，照搬没有意义。真正该借鉴的是它的**思路**：对"有副作用的提交类工具"（`profile_patch_commit`、`data_import_artifact`）做版本比较与交换（compare-and-swap），防止基于过期观测的提交。
5. **无保留策略**。DSH 的会话日志、投影缓存**都没有删除/保留 API**（文档明确承认）。我们在 `run_messages` 上已经有静默 5000 条截断，这是比"没有保留策略"更糟的形态（不可见）。应该显式定义保留策略并作为事实返回，而不是静默截断。

---

## 8. 建议的落地顺序

| 阶段 | 内容 | 依赖 |
|---|---|---|
| 1 | P0-1 历史重建 + 配对修复；P2-2 schema 单一来源与版本表 | 无 |
| 2 | P0-2 token 估算回退 + 前移压力检查 + 按模型窗口 | 阶段 1 的 surface 表达 |
| 3 | P0-2 总结式压缩 + 工具结果裁剪器 | 阶段 2 |
| 4 | P1-4 重试幂等化 + 失败配对 + `continuationFacts` 修复 | 无（可与阶段 2 并行） |
| 5 | P1-1 并发分类 + 超时包装 + 可用性去阻塞 | 无 |
| 6 | P1-3 权限准入收敛 + fail closed + 审计 | 无 |
| 7 | P2-1 事件命名 + 游标回退 + 编解码收敛 | 阶段 1 |
| 8 | P2-3 token/耗时持久化与统计事实 | 阶段 2 |
| 9 | P3 按产品需要选择性推进（jobs → fork 子代理 → skills） | 阶段 1 |

---

## 附录 A：DSH 关键设计索引

| 主题 | 包 |
|---|---|
| 会话日志 / 表面 / 派生历史 | `dsh-session`、`dsh-session-format`、`dsh-session-format-catalog` |
| 持久化 seam / JSONL 后端 | `dsh-session-persistence`、`dsh-session-persistence-jsonl` |
| 投影 / 缓存 / 查询 | `dsh-session-projection`、`dsh-session-projection-cache`、`dsh-session-query`、`dsh-session-query-sqlite` |
| Agent 循环 / 取消 / inbox | `dsh-agent-loop`、`dsh-agent` |
| 提示组装 / 人格 / 指令注入 | `dsh-system-prompt`、`dsh-persona`、`dsh-agent-instructions` |
| 压缩 / 裁剪 / 图片卸载 | `dsh-compaction`、`dsh-compaction-basic`、`dsh-compaction-tool-result-pruner`、`dsh-compaction-image-offload` |
| Token 计量 | `dsh-token-meter` |
| 工具契约 / 管线 / 超时 / 重复提醒 | `dsh-tools`、`dsh-tool-call-timeout-policy`、`dsh-repeat-tool-reminder` |
| 结果裁剪 / 落盘 | `dsh-output-retention`、`dsh-spill`、`dsh-spill-policy` |
| 沙箱 / 审批 / 预设 / 观测策略 | `dsh-sandbox-policy`、`dsh-user-approval`、`dsh-permission-presets`、`dsh-fs-observation-policy` |
| 子代理 / 后台任务 | `dsh-subagent`、`dsh-tool-subagent`、`dsh-jobs`、`dsh-tool-jobs` |
| 目标 / 计划模式 / 技能 / 命令 / 工作流 | `dsh-goal`、`dsh-goal-round-driver`、`dsh-plan-mode`、`dsh-skill`、`dsh-commands`、`dsh-workflow` |
| 可观测性 / 自检 | `dsh-session-telemetry`、`dsh-session-stats`、`dsh-otel`、`dsh-invariants` |
| 传输 / 前端 | `dsh-api-gateway`、`dsh-api-session-controller`、`dsh-client-ui-renderer`、`dsh-client-ui-tool`、`dsh-client-ui-trajectory` |

## 附录 B：证据索引（本仓库）

| 结论 | 位置 |
|---|---|
| 内存历史、压缩常量 | `server/agent/engine.go:23-32`、`engine.go:51-56` |
| 前向裁剪 + 仅省略计数 | `engine.go:113-132`、`engine.go:173-190`、`engine.go:328-332` |
| 压缩触发用已发送请求的 usage | `engine.go:350`、`engine.go:711` |
| usage 为 0 则跳过压缩 | `engine.go:117`、`engine.go:177` |
| 串行工具派发 | `engine.go:437` |
| 不分幂等的工具重试 | `engine.go:88-110` |
| 结果契约校验 / ui_summary 剥离 | `engine.go:740-795` |
| 结果解析失败即终止 run | `engine.go:575-579` |
| `continuationFacts` 永不重置 | `engine.go:302`、`engine.go:327`、`engine.go:371` |
| 可用性探测位置与派发不校验 | `engine.go:233-251`、`engine.go:287-290`、`engine.go:438` |
| 历史不还原 | `server/handler/session_runtime.go:46-68`、`runtime_state.go:143-199` |
| 重启即失败 | `session_runtime.go:90-98` |
| 事件持久化（素材齐全） | `server/handler/runtime_events.go:492-568` |
| SQLite 内联 DDL / Postgres goose | `server/metadata/store.go:62-323`、`server/metadata/postgres.go:56-74` |
| SSE 匿名事件 / 未知游标全量重放 | `server/handler/sse.go:310-318`、`sse.go:251-256` |
| 5000 条静默截断 | `server/repository/sqlite/message_repo.go:27,75` |
| 根 run 无 token 记账 | `server/agent/llm.go:338-340,730-732`、`server/handler/runtime_events.go:214-222` |
| 单进程存活判定 | `server/session/manager.go:277-282` |
| worker 结果未剥离 ui_summary / 重复 history_window | `server/agent/worker.go:570-575`、`engine.go:128-131` |
