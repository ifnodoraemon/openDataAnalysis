# MaaS Production Architecture Contract

This document defines the target backend contract for running Open Data Analysis as a MaaS product. The local Docker Compose setup remains a development profile only.

## Status

The current default backend is intentionally marked as development mode:

- `METADATA_STORE=sqlite`
- `STORAGE_PROVIDER=local`
- `RUN_BACKEND=inprocess`
- `ANALYSIS_STORE=session_sqlite`
- `PYTHON_ARTIFACT_STORE=executor_local`

When `DEPLOYMENT_MODE=production`, the server must fail closed if any of these local or single-process backends are still configured. A shared filesystem is not a production scaling strategy for this product.

## Target Contract

| Area | Production contract |
|---|---|
| API server | Stateless request handling. It authenticates, validates, persists commands, and publishes/subscribes to events. It does not own active run execution. |
| Run execution | Durable run/job backend with queued/running/waiting/finalized/failed states, leases, heartbeats, cancellation, retry, and recovery. |
| Workers | Horizontally scalable worker pool. Workers claim leased work, materialize required inputs, emit progress events, and persist state transitions. |
| Metadata | Shared transactional store, expected to be PostgreSQL. SQLite is only a development store. |
| Object storage | S3-compatible or equivalent object store for uploads, reports, chart images, Python outputs, exports, and large intermediate artifacts. |
| Analysis state | Worker-local scratch state is allowed only when it can be rebuilt from durable source snapshots and manifests. Session SQLite files cannot be the source of truth. |
| Events | Persisted event log plus fanout channel. WebSocket clients subscribe to run/session events; WebSocket handlers do not execute model runs. |
| Python execution | Executors do not serve durable artifacts from local disk. Generated files are stored through object storage or a server-side artifact ingestion API. |
| Identity | Managed user/workspace provisioning, token revocation or short-lived access tokens, distributed rate limits, role-aware authorization, and audit logs. |

## Runtime Rules

1. API and WebSocket handlers may create commands, runs, and subscriptions, but must not directly own long-running analysis execution.
2. A server restart or replica change must not by itself fail a run. Failure should come from job state, timeout, cancellation, or a terminal worker error.
3. A worker may use local disk as scratch space, but all user-visible or resumable artifacts must have durable object keys.
4. Cleanup must be driven by durable metadata and object storage lifecycle, not by whichever process currently has a session in memory.
5. Production origins must be explicit. Wildcard and localhost origins are development-only.

## Migration Order

1. Keep development mode working, but make production mode fail closed while local backends remain selected.
2. Move metadata repositories from SQLite to PostgreSQL behind the existing repository interfaces.
3. Introduce a durable run/job backend and worker lease loop. WebSocket becomes event subscription only.
4. Add S3-compatible object storage and route uploads, reports, Python artifacts, and exports through it.
5. Replace session SQLite as source of truth with durable snapshot manifests and worker-materialized analysis scratch state.
6. Replace default user bootstrap with managed identity and workspace provisioning.

## Non-Goals

- Do not scale by requiring sticky WebSocket sessions to a specific API process.
- Do not treat shared local volumes as durable multi-tenant storage.
- Do not add hidden workflow prompts to compensate for missing backend state.
- Do not make tools return `next_action` advice instead of facts.
