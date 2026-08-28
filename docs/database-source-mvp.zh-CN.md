# 数据库数据源边界

更新日期：2026-08-11

本文档记录当前数据库数据源实现和边界。历史 source-first 计划已合并进本文档；过期草案不再保留。

## 当前状态

已支持 PostgreSQL 和 MySQL workspace data source，并以 snapshot import 方式导入到当前 session 的 SQLite 分析库。

后端已引入 `SourceConnector` 抽象：

- `file_upload`、`postgres_connection` 和 `mysql_connection` 都通过 connector 执行 catalog/test/import 等运行时能力。
- connector 负责 config 规范化、credential 加密、公开配置序列化和运行时导入。
- 导入后的 schema 提取、snapshot completion、structural profile 创建、截断/估计 warning 写入走统一收尾管线。
- 每次导入使用独立分析表；成功完成后才切换 active binding，随后清理旧版本。失败导入不会覆盖或删除旧的 active snapshot。
- SQL 配置已从专用 `database_connections` 升级为通用 `source_configs`，后续新增商业数据源不再需要新增专用配置表。

当前不支持：

- live upstream query
- 写回数据库
- 任意数据库类型
- 跨数据库实时 join
- 自动修改上游 schema

## 为什么先做 Snapshot Import

当前分析执行层是 session-scoped SQLite。SQL 表/视图先导入成固定 snapshot，再由 agent 使用同一套 `data_query_sql`、图表和报告工具分析。

这样做的原因：

- 分析可复现，报告能追溯到导入时刻。
- 权限、超时、行数上限和资源消耗更容易控制。
- 文件上传和数据库导入在 agent 观察面中保持同质。
- 不把上游数据库暴露为任意 SQL 执行面。

## 领域模型

| 实体 | 作用 |
|---|---|
| `DataSource` | 工作区级数据源；当前包括 `file_upload`、`postgres_connection` 和 `mysql_connection` |
| `SourceConfig` | connector 类型、公开配置 JSON、加密 credential、测试状态 |
| `SourceSnapshot` | 某个 source 在某个 session 中导入到 SQLite 的固定快照 |
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
- 只允许导入 allowlist 中的 schema/table/view。
- catalog API 只返回 allowlist 对象，不扫描整库暴露元数据。

## 导入边界

默认配置：

```env
SQL_IMPORT_ROW_LIMIT=1000000
```

行为：

- `0` 表示不限制导入行数。
- 大于 `0` 时，导入查询使用 `LIMIT row_limit + 1` 探测是否被截断。
- 如果超过上限，snapshot 保存 `import_truncated=true` 和 `import_row_limit`。
- profile 和 UI 会明确显示截断状态，报告不应把受限快照包装成全量事实。

持久化 profile 对已导入 snapshot 做精确结构观察。`data_describe_table` 的调用方必须显式提供 `sample_rows`：`0` 表示精确统计，正数表示有界结构采样；工具会原样返回 source rows、sample rows、method 和 estimated 状态。运行时不按数据规模替模型选择该参数。

## 语义画像和确认

导入完成后会生成 `SemanticProfile`。它只保存结构观测：schema、声明类型、行列数、非空率、distinct/sample values、采样方法和操作性 warning。runtime 不从列名或值格式推断数值含义、时间列、业务别名、指标、单位、join 或主时间列，也不把未确认解释写回 profile。

用户确认通过 `SemanticConfirmation` 保存：

- `session` 范围只影响当前会话。
- `workspace` 范围可在 schema signature 匹配时复用。
- confirmation 与观测 profile 分开保存，不自动合并或覆盖观测事实。

workspace 范围的确认还会沉淀为 `SemanticAsset`：

- 每次确认的完整 JSON patch 以不透明确认 ID 保存为一个资产；运行时不拆分、合并或解释其中字段。
- 同 workspace、同 schema signature 的资产由 observation tool 暴露，runtime 不自动应用。
- 语义资产是复用事实，不是关键词触发器，也不规定 agent 下一步流程。

关键事件会写入 `AuditEvent`：

- 数据源导入完成后记录 source、snapshot、目标分析表、行列数、截断状态和 profile ID。
- profile 确认和语义资产 upsert 会记录 actor、scope、schema signature 和资产键。

## API

- `POST /api/data-sources`：创建 connector-backed source，请求体使用 `source_type`、`config`、`credential`。
- `GET /api/data-sources`：列出工作区 sources。
- `PUT /api/data-sources/{sourceID}`：更新 source 名称、connector config 或 credential。
- `DELETE /api/data-sources/{sourceID}`：删除 workspace source，并移除相关 snapshot/profile。
- `POST /api/data-sources/{sourceID}/test`：测试连接和 allowlist。
- `GET /api/data-sources/{sourceID}/catalog`：返回 allowlist 对象。
- `POST /api/data-sources/{sourceID}/import`：导入 allowlist 对象到 session snapshot。
- `GET /api/sessions/{sessionID}/sources`：查看当前 session source/snapshot/profile 摘要。
- `GET /api/semantic-profiles/{profileID}`：查看 profile 详情。
- `POST /api/semantic-profiles/{profileID}/confirm`：保存确认或覆盖。

## Source Connector 边界

Connector 负责数据源类型相关的运行时能力：

- `NormalizeConfig`：校验并规范化 connector 配置，必要时加密 credential。
- `PublicConfig`：返回不含 secret 的配置摘要和最近测试状态。
- `Test`：验证文件、连接或外部系统对象是否可访问。
- `Catalog`：返回当前 source 可导入对象，且只暴露允许范围。
- `Import`：把指定对象物化到 session analysis SQLite。

Connector 不负责 agent 决策、不直接生成报告，也不把上游 live query 暴露给 agent。所有 connector import 成功后都必须产出 `SourceSnapshot` 和尽可能完整的 `SemanticProfile`。

## Agent 工具面

主要 observation tools：

- `state_session_sources_inspect`
- `state_semantic_profile_inspect`
- `state_governance_inspect`
- `state_source_confirm_profile`

分析工具仍只面向 session SQLite snapshot：

- `data_list_tables`
- `data_describe_table`
- `data_query_sql`

Agent 不直接生成上游数据库 SQL。

`state_governance_inspect` 只返回通用数据治理事实，例如 snapshot 状态、导入截断、profile warning、授权补丁和可复用资产数量；它不返回 `next_action`，也不按固定行业或工作流触发行为。

## 后续方向

后续如果要支持 live query 或更多 SaaS/API connector，应作为独立执行层或 connector 能力设计，并单独处理：

- 上游 SQL 权限和超时。
- pushdown 与 session snapshot 的一致性。
- 报告复现和数据期间标注。
- 大表筛选、分区、增量导入或 DuckDB/ClickHouse 等执行引擎选择。
