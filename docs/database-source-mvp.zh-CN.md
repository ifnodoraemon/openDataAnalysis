# 数据库数据源边界

更新日期：2026-08-11

本文档记录当前数据库数据源实现和边界。历史 source-first 计划已合并进本文档；过期草案不再保留。

## 当前状态

PostgreSQL 和 MySQL workspace data source 以只读实时直连方式支持：分析查询直接在源库执行。上传文件（CSV/XLSX）仍导入到当前 session 的 SQLite 分析库。

后端通过 `SourceConnector` 抽象支撑：

- `file_upload`、`postgres_connection` 和 `mysql_connection` 都通过 connector 执行 catalog/test/绑定（文件为导入）等运行时能力。
- connector 负责 config 规范化、credential 加密、公开配置序列化和各自的运行时能力。
- 文件导入走统一收尾管线（schema 提取、snapshot completion、structural profile、截断/估计 warning）；live 绑定走统一收尾管线（catalog 元数据、profile 创建、激活）。
- 每个导入文件使用独立分析表；成功完成后才切换 active binding，随后清理旧版本。失败的绑定不会覆盖或删除旧的 active snapshot。
- SQL 配置使用通用 `source_configs`，后续新增商业数据源不再需要新增专用配置表。

当前不支持：

- 写回数据库
- 任意数据库类型
- 跨源实时 join
- 自动修改上游 schema

每条实时查询都是单条只读语句，只触碰一个已绑定数据源。

## 为什么数据库走实时直连

导入行数有上限，大表会被截断，恰好在大规模数据上产生错误结论。只读直连把聚合和过滤下推给源库引擎，并保证数据实时。补偿机制是只读事务、语句超时、行数上限，以及"单条查询只触碰一个源"的显式事实。实时数据源的可复现性作为 live 属性向模型和用户披露，而不是靠复制数据来保证。

## 领域模型

| 实体 | 作用 |
|---|---|
| `DataSource` | 工作区级数据源；当前包括 `file_upload`、`postgres_connection` 和 `mysql_connection` |
| `SourceConfig` | connector 类型、公开配置 JSON、加密 credential、测试状态 |
| `SourceSnapshot` | source 在某个 session 中绑定的对象：`mode=imported`（文件行已入 session SQLite）或 `mode=live`（上游对象，不复制行） |
| `SessionSourceBinding` | session 当前绑定的 source/object -> active snapshot |
| `SemanticProfile` | schema、采样方式、列统计和 warning 等观测事实 |
| `SemanticConfirmation` | 用户明确提交的 session/workspace 级 JSON patch 与授权来源 |
| `SemanticAsset` | workspace 级可复用 patch 事实，由用户确认沉淀，按 schema signature 查询 |
| `AuditEvent` | 关键数据操作和语义资产变更的审计事件 |

## SQL 连接边界

- PostgreSQL 使用 `pgx/v5/stdlib`，上游连接设置 `default_transaction_read_only=on`。
- MySQL 使用 `go-sql-driver/mysql`，运行时只生成 allowlist 对象的 `SELECT *` import 查询。
- PostgreSQL 的 `ssl_mode` 和 MySQL 的 `tls_mode` 都必须显式提供；运行时不根据 host、端口或环境猜测传输模式。
- 创建 SQL 数据源时要求 `AUTH_SECRET` 长度至少 32，用于 AES-GCM 加密密码。
- 只有 allowlist 中的 schema/table/view 可以绑定到会话做实时查询。
- catalog API 只返回 allowlist 对象，不扫描整库暴露元数据。

## 导入与直连的边界

两类数据源采用不同模型：

- `file_upload`（CSV/XLSX）导入到会话本地 SQLite 分析库，profile 从导入行做精确结构统计。
- `postgres_connection` 和 `mysql_connection` 不导入任何数据行。绑定一个 allowlist 对象会创建 live 快照记录（`mode=live`）、一份来自上游 catalog 的结构 profile，以及一条会话绑定；分析查询直接在源库执行。

文件导入默认不做行数上限；`SQL_IMPORT_ROW_LIMIT` 环境变量已移除。

## 实时查询边界

`data_query_sql` 携带 `source_id` 时，在源库直接执行一条经过校验的只读语句：

- 只接受单条 `SELECT`/`WITH`；DML 与 DDL 一律拒绝。
- PostgreSQL 在 `BEGIN TRANSACTION READ ONLY` 中执行并设置 `statement_timeout`，连接池同时设置 `default_transaction_read_only=on`；MySQL 在 `START TRANSACTION READ ONLY` 中执行并设置 `max_execution_time`。
- 结果上限 200 行；超限直接报错，不做静默截断。
- 查询使用源库方言的 schema 限定表名；单条查询只触碰一个数据源，跨源 join 不受支持，会返回事实性错误。
- 查询以所配置数据库账号的权限执行；该账号应当只读且最小授权。

`data_list_tables` 携带 `source_id` 时列出已绑定的实时对象及引擎行数估算。`data_describe_table` 携带 `source_id` 时从 live profile 返回结构列事实；正数 `sample_rows` 会取一次有界的上游采样，`0` 不发起上游查询——对实时源不做精确全表统计。

## 语义画像和确认

导入完成后会生成 `SemanticProfile`。它只保存结构观测：schema、声明类型、行列数、非空率、distinct/sample values、采样方法和操作性 warning。live 绑定生成 catalog 型 profile：来自上游 catalog 的列名与声明类型、引擎行数估算，以及 `live` 画像模式；由于不复制数据行，不包含逐列值统计。runtime 不从列名或值格式推断数值含义、时间列、业务别名、指标、单位、join 或主时间列，也不把未确认解释写回 profile。

用户确认通过 `SemanticConfirmation` 保存：

- `session` 范围只影响当前会话。
- `workspace` 范围可在 schema signature 匹配时复用。
- confirmation 与观测 profile 分开保存，不自动合并或覆盖观测事实。

workspace 范围的确认还会沉淀为 `SemanticAsset`：

- 每次确认的完整 JSON patch 以不透明确认 ID 保存为一个资产；运行时不拆分、合并或解释其中字段。
- 同 workspace、同 schema signature 的资产由 observation tool 暴露，runtime 不自动应用。
- 语义资产是复用事实，不是关键词触发器，也不规定 agent 下一步流程。

关键事件会写入 `AuditEvent`：

- 数据源绑定/导入完成后记录 source、snapshot、目标分析表（导入）或上游对象（实时）、行列数、截断状态（导入）和 profile ID。
- profile 确认和语义资产 upsert 会记录 actor、scope、schema signature 和资产键。

## API

- `POST /api/data-sources`：创建 connector-backed source，请求体使用 `source_type`、`config`、`credential`。
- `GET /api/data-sources`：列出工作区 sources。
- `PUT /api/data-sources/{sourceID}`：更新 source 名称、connector config 或 credential。
- `DELETE /api/data-sources/{sourceID}`：删除 workspace source，并移除相关 snapshot/profile。
- `POST /api/data-sources/{sourceID}/test`：测试连接和 allowlist。
- `GET /api/data-sources/{sourceID}/catalog`：返回 allowlist 对象。
- `POST /api/data-sources/{sourceID}/import`：将一个 allowlist 对象绑定到会话。SQL 数据源创建实时绑定；文件数据源导入快照。
- `GET /api/sessions/{sessionID}/sources`：查看当前 session source/snapshot/profile 摘要。
- `GET /api/semantic-profiles/{profileID}`：查看 profile 详情。
- `POST /api/semantic-profiles/{profileID}/confirm`：保存确认或覆盖。

## Source Connector 边界

Connector 负责数据源类型相关的运行时能力：

- `NormalizeConfig`：校验并规范化 connector 配置，必要时加密 credential。
- `PublicConfig`：返回不含 secret 的配置摘要和最近测试状态。
- `Test`：验证文件、连接或外部系统对象是否可访问。
- `Catalog`：返回当前 source 可绑定对象，且只暴露允许范围。
- SQL connector 另实现 live 能力：`FetchLiveObjectMetadata`（catalog 列结构 + 行数估算）与 `ExecuteLiveQuery`（只读事务 + 超时 + 行数上限的直连执行）。
- 文件 connector 另实现 `Import`：把指定对象物化到 session analysis SQLite。

Connector 不负责 agent 决策、不直接生成报告。所有绑定成功后都必须产出 `SourceSnapshot`（`mode=imported` 或 `mode=live`）和结构 `SemanticProfile`。

## Agent 工具面

主要 observation tools：

- `state_session_sources_inspect`
- `state_semantic_profile_inspect`
- `state_governance_inspect`
- `state_source_confirm_profile`

分析工具默认面向 session SQLite（导入文件），并支持通过显式 `source_id` 路由到一个已绑定的实时数据源：

- `data_list_tables`：无 `source_id` 列出本地表；携带 `source_id` 列出该源已绑定的实时对象。
- `data_describe_table`：无 `source_id` 做本地结构统计；携带 `source_id` 返回 live 结构事实，正数 `sample_rows` 触发有界上游采样。
- `data_query_sql`：无 `source_id` 执行 SQLite 方言查询；携带 `source_id` 在源库方言下直连执行（只读事务、语句超时、200 行上限）。单条查询只触碰一个数据源。

`state_governance_inspect` 只返回通用数据治理事实，例如 snapshot 状态、导入截断、profile warning、授权补丁和可复用资产数量；它不返回 `next_action`，也不按固定行业或工作流触发行为。

## 后续方向

后续新增 connector 类型（例如 Hive、ClickHouse 等引擎）沿用同一 live 只读 connector 契约：显式配置、allowlist 绑定、方言级只读执行（语句超时 + 行数上限）。这些能力不从现有 connector 类型猜测。针对变化的实时数据的报告可复现性是模型与用户的显式责任，并通过报告事实披露。
