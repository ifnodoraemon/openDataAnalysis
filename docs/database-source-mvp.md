# Database Source Boundaries

Updated: 2026-08-11

This document records the current database-source implementation and its boundaries. Obsolete source-first drafts have been consolidated here.

## Current state

PostgreSQL and MySQL workspace sources are supported through snapshot imports into the current session's SQLite analysis database.

The backend exposes a `SourceConnector` boundary:

- `file_upload`, `postgres_connection`, and `mysql_connection` use connector capabilities for catalog, test, and import operations.
- A connector validates configuration, encrypts credentials, serializes public configuration, and performs its source-specific import.
- Schema extraction, snapshot completion, structural profile creation, and truncation or estimate warnings share one completion pipeline.
- Every import uses a distinct analysis table. The active binding changes only after successful completion, then superseded snapshots are cleaned up. A failed import does not replace the active snapshot.
- SQL configuration uses generic `source_configs`, so a new connector does not require another type-specific configuration table.

The runtime does not currently provide live upstream queries, database writes, arbitrary database drivers, live cross-database joins, or upstream schema changes.

## Why snapshot import

The analysis execution layer is session-scoped SQLite. A source object becomes a fixed snapshot before the model uses the same SQL, chart, and report tools that it uses for uploaded files. This keeps analysis reproducible, makes limits and permissions enforceable, gives files and databases the same observation surface, and avoids exposing an arbitrary upstream SQL endpoint.

## Domain model

| Entity | Purpose |
|---|---|
| `DataSource` | Workspace source identity and exact connector type |
| `SourceConfig` | Public connector configuration, encrypted credentials, and test state |
| `SourceSnapshot` | Fixed source object imported into one session's SQLite database |
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
- Only allowlisted schema, table, or view objects can be imported.
- Catalog operations return the allowlist and do not scan an entire database.

## Import boundary

The default limit is:

```env
SQL_IMPORT_ROW_LIMIT=1000000
```

`0` means unlimited. A positive limit reads at most `row_limit + 1` rows to observe truncation. A truncated snapshot persists `import_truncated=true` and `import_row_limit`; profile and UI surfaces disclose those facts.

Persisted profiles describe imported snapshots. Callers of `data_describe_table` provide `sample_rows` explicitly: `0` requests exact statistics and a positive value requests bounded structural sampling. The tool returns source rows, sampled rows, method, and estimate state without choosing that trade-off for the model.

## Semantic profiles and confirmations

An imported snapshot produces a `SemanticProfile` containing structural observations: schema, declared types, row and column counts, null rates, distinct or sampled values, sampling method, and operational warnings. The runtime does not infer metrics, units, joins, business aliases, primary time columns, or numeric meaning from names or value formats.

A `SemanticConfirmation` stores an explicit user-authorized patch. Session scope affects one session; workspace scope makes the patch discoverable when the schema signature matches. Confirmations remain separate from observations and are not applied automatically.

A workspace confirmation also creates a `SemanticAsset`. The complete JSON patch is stored under an opaque confirmation identity; the runtime does not split, merge, or interpret its fields. Matching assets are observation facts, not keyword triggers or workflow instructions.

Audit events record import completion, source and snapshot identity, target table, shape, truncation, profile identity, actor, scope, schema signature, and semantic asset identity where applicable.

## API

- `POST /api/data-sources`: create a connector-backed source.
- `GET /api/data-sources`: list workspace sources.
- `PUT /api/data-sources/{sourceID}`: update exact source name, configuration, or credentials.
- `DELETE /api/data-sources/{sourceID}`: delete a workspace source and related snapshots or profiles.
- `POST /api/data-sources/{sourceID}/test`: test configured access.
- `GET /api/data-sources/{sourceID}/catalog`: list declared importable objects.
- `POST /api/data-sources/{sourceID}/import`: import one declared object into a session snapshot.
- `GET /api/sessions/{sessionID}/sources`: inspect source, snapshot, and profile summaries.
- `GET /api/semantic-profiles/{profileID}`: inspect a profile.
- `POST /api/semantic-profiles/{profileID}/confirm`: persist an authorized patch.

## Connector boundary

`NormalizeConfig` validates exact connector configuration and encrypts credentials when needed. `PublicConfig` returns non-secret facts. `Test` observes configured access. `Catalog` returns the explicitly allowed import surface. `Import` materializes one selected object into session SQLite.

Connectors do not decide agent behavior, generate reports, or expose live upstream query access. Every successful import produces a `SourceSnapshot` and structural profile facts.

## Agent tool surface

Observation tools include `state_session_sources_inspect`, `state_semantic_profile_inspect`, `state_governance_inspect`, and the user-authorized action `state_source_confirm_profile`. Analysis tools operate only on session snapshots: `data_list_tables`, `data_describe_table`, and `data_query_sql`.

`state_governance_inspect` returns generic governance facts such as snapshot state, truncation, profile warnings, authorized patches, and reusable asset counts. It does not return `next_action` or select behavior from an industry or workflow.

## Future direction

Live query or additional connector types require an explicit execution boundary for upstream permissions, timeouts, pushdown consistency, report reproducibility, data-period metadata, partitioning, incremental import, and execution-engine selection. Those capabilities are not guessed from the current connector type.
