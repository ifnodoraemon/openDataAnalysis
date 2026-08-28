# Database Source Boundaries

Updated: 2026-08-11

This document records the current database-source implementation and its boundaries. Obsolete source-first drafts have been consolidated here.

## Current state

PostgreSQL and MySQL workspace sources are supported as read-only live connections: analysis queries run directly against the upstream database. Uploaded files (CSV/XLSX) are imported into the session's SQLite analysis database.

The backend exposes a `SourceConnector` boundary:

- `file_upload`, `postgres_connection`, and `mysql_connection` use connector capabilities for catalog, test, binding, and (for files) import operations.
- A connector validates configuration, encrypts credentials, serializes public configuration, and performs its source-specific runtime capability.
- File imports share one completion pipeline for schema extraction, snapshot completion, structural profile creation, and truncation or estimate warnings. Live bindings share one completion pipeline for catalog metadata, profile creation, and activation.
- Every imported file uses a distinct analysis table. The active binding changes only after successful completion, then superseded snapshots are cleaned up. A failed bind does not replace the active snapshot.
- SQL configuration uses generic `source_configs`, so a new connector does not require another type-specific configuration table.

The runtime does not provide database writes, arbitrary database drivers, live cross-source joins, or upstream schema changes. Every live query is a single read-only statement against one bound source.

## Why live connection for databases

Importing rows truncates large tables and produces wrong analysis exactly where scale matters. Live read-only execution pushes aggregation and filtering down to the source engine and keeps data current. The compensating controls are the read-only transaction, statement timeout, row cap, and the explicit fact that a query touches exactly one source. Reproducibility against changing live data is disclosed as a property of live sources rather than guaranteed by copying.

## Domain model

| Entity | Purpose |
|---|---|
| `DataSource` | Workspace source identity and exact connector type |
| `SourceConfig` | Public connector configuration, encrypted credentials, and test state |
| `SourceSnapshot` | Source object bound to one session: `mode=imported` (file rows in session SQLite) or `mode=live` (upstream object, no copied rows) |
| `SessionSourceBinding` | Current source-object identity and active snapshot for a session |
| `SemanticProfile` | Observed schema, sampling facts, column statistics, and warnings |
| `SemanticConfirmation` | Explicit user-authorized JSON patch and its session or workspace scope |
| `SemanticAsset` | Reusable workspace patch fact queried by schema signature |
| `AuditEvent` | Durable fact about a material data or semantic-state change |

## SQL connection boundary

- PostgreSQL uses `pgx/v5/stdlib` with `default_transaction_read_only=on`.
- MySQL uses `go-sql-driver/mysql`; import queries select only declared allowlist objects.
- PostgreSQL `ssl_mode` and MySQL `tls_mode` are explicit. The runtime does not infer transport mode from a host, port, or environment.
- Creating a SQL source requires an `AUTH_SECRET` of at least 32 characters for AES-GCM credential encryption.
- Only allowlisted schema, table, or view objects can be bound to a session for live queries.
- Catalog operations return the allowlist and do not scan an entire database.

## Import versus live connection

The two source models are deliberately different:

- `file_upload` (CSV/XLSX) is imported into the session-local SQLite analysis database. Import produces exact structural profiles from imported rows.
- `postgres_connection` and `mysql_connection` never import rows. Binding one allowlisted object creates a live snapshot record (`mode=live`), a structural profile from the upstream catalog, and a session binding. Analysis queries run directly against the upstream database.

File imports are not row-capped by default; the `SQL_IMPORT_ROW_LIMIT` environment variable has been removed.

## Live query boundary

`data_query_sql` with a `source_id` executes one validated read-only statement directly on the upstream database:

- Only a single `SELECT`/`WITH` statement is accepted; DML and DDL are rejected.
- PostgreSQL runs inside `BEGIN TRANSACTION READ ONLY` with `statement_timeout`; the pool also sets `default_transaction_read_only=on`. MySQL runs inside `START TRANSACTION READ ONLY` with `max_execution_time`.
- Results are capped at 200 rows; exceeding the limit fails the query instead of truncating silently.
- The query uses upstream schema-qualified table names in the source dialect. A single query touches exactly one source; joining across sources is not supported and returns a factual error.
- Queries execute with the permissions of the configured database account; the account should be read-only and minimally scoped.

`data_list_tables` with a `source_id` lists bound live objects with engine row-count estimates. `data_describe_table` with a `source_id` serves structural column facts from the live profile; a positive `sample_rows` fetches a bounded upstream sample, while `0` skips the upstream query because exact full-table statistics are not supported against live sources.

## Semantic profiles and confirmations

An imported snapshot produces a `SemanticProfile` containing structural observations: schema, declared types, row and column counts, null rates, distinct or sampled values, sampling method, and operational warnings. A live binding produces a catalog-based profile: column names and declared types from the upstream catalog, an engine row-count estimate, and a `live` profile mode; per-column value statistics are absent because no rows are copied. The runtime does not infer metrics, units, joins, business aliases, primary time columns, or numeric meaning from names or value formats.

A `SemanticConfirmation` stores an explicit user-authorized patch. Session scope affects one session; workspace scope makes the patch discoverable when the schema signature matches. Confirmations remain separate from observations and are not applied automatically.

A workspace confirmation also creates a `SemanticAsset`. The complete JSON patch is stored under an opaque confirmation identity; the runtime does not split, merge, or interpret its fields. Matching assets are observation facts, not keyword triggers or workflow instructions.

Audit events record bind/import completion, source and snapshot identity, target table (imported) or upstream object (live), shape, truncation for imports, profile identity, actor, scope, schema signature, and semantic asset identity where applicable.

## API

- `POST /api/data-sources`: create a connector-backed source.
- `GET /api/data-sources`: list workspace sources.
- `PUT /api/data-sources/{sourceID}`: update exact source name, configuration, or credentials.
- `DELETE /api/data-sources/{sourceID}`: delete a workspace source and related snapshots or profiles.
- `POST /api/data-sources/{sourceID}/test`: test configured access.
- `GET /api/data-sources/{sourceID}/catalog`: list declared importable objects.
- `POST /api/data-sources/{sourceID}/import`: bind one declared object to a session. SQL sources create a live binding; file sources import a snapshot.
- `GET /api/sessions/{sessionID}/sources`: inspect source, snapshot, and profile summaries.
- `GET /api/semantic-profiles/{profileID}`: inspect a profile.
- `POST /api/semantic-profiles/{profileID}/confirm`: persist an authorized patch.

## Connector boundary

`NormalizeConfig` validates exact connector configuration and encrypts credentials when needed. `PublicConfig` returns non-secret facts. `Test` observes configured access. `Catalog` returns the explicitly allowed binding surface. SQL connectors implement live object metadata and read-only live query execution; the file connector implements `Import` into session SQLite.

Connectors do not decide agent behavior or generate reports. Every successful bind produces a `SourceSnapshot` (`mode=imported` or `mode=live`) and structural profile facts.

## Agent tool surface

Observation tools include `state_session_sources_inspect`, `state_semantic_profile_inspect`, `state_governance_inspect`, and the user-authorized action `state_source_confirm_profile`. Analysis tools are `data_list_tables`, `data_describe_table`, and `data_query_sql`; each addresses the session-local SQLite database by default or one live-bound source through an explicit `source_id`. The `data_transform_materialize` tool has been removed.

`state_governance_inspect` returns generic governance facts such as snapshot state, truncation, profile warnings, authorized patches, and reusable asset counts. It does not return `next_action` or select behavior from an industry or workflow.

## Future direction

Additional connector types (for example Hive, ClickHouse, or other engines) follow the same live read-only connector contract: explicit configuration, allowlisted binding, dialect-specific read-only execution with statement timeouts and row caps. Those capabilities are not guessed from the current connector type. Report reproducibility against changing live data remains an explicit responsibility of the model and the user, and is disclosed as such in report facts.
