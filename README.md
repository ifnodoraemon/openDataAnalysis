# OpenDataAnalysis

[中文说明](README.zh-CN.md)

Interactive data analysis for tabular data. Users upload CSV/Excel files or import SQL source snapshots, then an agent inspects data, runs SQL/Python when needed, creates charts, and produces reports.

![OpenDataAnalysis UI](docs/images/screenshot.png)

## Current Capabilities

- Agent runtime with tool calling and self-directed planning, not a fixed DAG.
- Auth, workspaces, sessions, runs, file ownership, and resumable reports.
- CSV/Excel upload plus PostgreSQL/MySQL workspace sources imported as bounded snapshots.
- Workspace semantic assets, general governance fact inspection, and audit events for critical data operations.
- Go backend, Vue 3 frontend, SQLite metadata DB, and per-session SQLite analysis DBs in development mode.
- Local object storage abstraction with explicit production guardrails for the S3-compatible migration.

## Quick Start

Local development is standardized on Docker Compose.

```bash
cp server/.env.example server/.env
# Configure LLM_PROVIDER / LLM_API_PROTOCOL / LLM_API_ENDPOINT / LLM_API_KEY / LLM_MODEL / AUTH_SECRET.
docker compose up -d --build
```

Open `http://localhost`.

For `LLM_PROVIDER=openai`, select `LLM_API_PROTOCOL=responses` or
`LLM_API_PROTOCOL=chat_completions` explicitly. Compatible-provider hostnames and
model names are not used to guess or rewrite the protocol or endpoint.

Common commands:

```bash
docker compose up -d --build --force-recreate
docker compose logs -f server
docker compose logs -f client
docker compose logs -f python-executor
docker compose down
```

## Deployment Modes

The default Docker Compose setup is a development profile. It uses local SQLite metadata, local object storage, in-process run execution, and session SQLite analysis scratch DBs. Python artifacts are ingested into the configured object-storage interface.

Production must be configured explicitly with `DEPLOYMENT_MODE=production`. In that mode the server fails closed while any local or single-process backend is still selected:

- `METADATA_STORE=sqlite`
- `STORAGE_PROVIDER=local`
- `RUN_BACKEND=inprocess`
- `ANALYSIS_STORE=session_sqlite`
- wildcard or localhost `CORS_ALLOWED_ORIGINS`

The target MaaS contract is documented in [`docs/maas-production-architecture.md`](docs/maas-production-architecture.md). Shared local volumes and process-local event subscriptions are not considered a production scaling strategy.

## Architecture

| Layer | Notes |
|---|---|
| Frontend | Vue 3 + Vite + Pinia |
| Backend | Go + Chi + SSE |
| Agent Runtime | Tool-calling ReAct loop; runtime exposes state/tools, model judges the path |
| Analysis DB | Development: one SQLite scratch DB per session. Production target: rebuildable worker scratch state from durable snapshot manifests |
| Metadata DB | Development: SQLite. Production target: PostgreSQL behind repository interfaces |
| Python Executor | Separate service for advanced analysis that SQL does not fit |
| Storage | Development: local object storage. Production target: S3-compatible object storage |
| Semantic Assets | Workspace-level reusable assets containing exact user-authorized patches and their schema signatures |
| Audit | Critical data imports, semantic confirmations, and semantic asset changes are persisted as audit events |

Development runtime data lives under `data/`:

- `data/metadata/`: metadata SQLite
- `data/cache/`: session SQLite DBs
- `data/storage/`: uploaded files and report objects
- `data/tmp/`: temp files
- `data/llm-debug/`: LLM traces

## Data Source Boundaries

| Type | Current status |
|---|---|
| CSV | Streaming batch import; observed headers and cell text are preserved exactly |
| Excel | Streaming batch import for a workbook with exactly one worksheet; observed headers and cell text are preserved exactly |
| PostgreSQL / MySQL | Read-only live connection; queries run directly against the upstream database (read-only transaction, statement timeout, 200-row cap); no data is imported |
| Live upstream query | Not supported; should be designed as a separate capability |

Every import is written to a snapshot-scoped analysis table. The current binding
changes only after schema facts and profile state have been persisted; a failed
replacement therefore leaves the previous snapshot readable. Persisted profiles
observe the imported snapshot exactly. The model explicitly selects `sample_rows`
when calling `data_describe_table`; the runtime does not choose sampling from a
dataset-size tier.

## Main API Surface

- `POST /api/auth/login`
- `POST /api/auth/switch-workspace`
- `GET /api/bootstrap`
- `GET /api/sessions`
- `GET /api/runs`
- `POST /api/upload?session_id=...`
- `GET /api/sessions/{sessionID}/sources`
- `POST /api/data-sources`
- `GET /api/data-sources`
- `POST /api/data-sources/{sourceID}/test`
- `GET /api/data-sources/{sourceID}/catalog`
- `POST /api/data-sources/{sourceID}/import`
- `GET /api/sse?session_id=...`

Agent observation tools also include `state_governance_inspect` for general data governance facts in the current session. It does not trigger a fixed workflow.

Connector-backed SQL source creation uses public `config` plus encrypted `credential`:

```json
{
  "name": "Analytics SQL",
  "source_type": "mysql_connection",
  "config": {
    "host": "db.example.com",
    "port": 3306,
    "database_name": "analytics",
    "tls_mode": "verify_identity",
    "username": "reader",
    "allowlist": [{ "schema": "analytics", "name": "orders", "kind": "table" }]
  },
  "credential": { "password": "secret" }
}
```

All APIs except `/api/auth/login` and `/api/health` require a token by default.

## Development Checks

```bash
cd server && go test ./...
cd client && npm run test
cd client && npm run build
cd client && npm run format:check
cd python-executor && python -m pytest test_sandbox.py
```

## Documentation

- `AGENTS.md`: repository-level agent constraints and prompt layering rules.
- `docs/product-first-principles.zh-CN.md`: product first principles.
- `docs/agentic-principles.md`: agent runtime design principles.
- `docs/maas-production-architecture.md`: production MaaS backend contract and migration order.
- `docs/building-agent-lessons.zh-CN.md`: concise agent runtime lessons, Chinese.
- `docs/database-source-mvp.md`: database source implementation and boundaries.
- `docs/benchmark.md`: regression evaluation plan.
- `samples/coverage_scenarios/README.zh-CN.md`: coverage scenario guide.

## License

MIT — see [LICENSE](LICENSE).
