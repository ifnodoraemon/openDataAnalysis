# MaaS Production Architecture Contract

This document defines the target backend contract for running OpenDataAnalysis as a MaaS product. The local Docker Compose setup remains a development profile only.

## Status

The current default backend is intentionally marked as development mode:

- `METADATA_STORE=sqlite`
- `STORAGE_PROVIDER=local`
- `RUN_BACKEND=inprocess`
- `ANALYSIS_STORE=session_sqlite`

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
| Events | Persisted event log plus fanout channel. SSE clients subscribe to run/session events; subscription handlers do not execute model runs. |
| Python execution | Executors do not serve durable artifacts from local disk. Generated files are stored through object storage or a server-side artifact ingestion API. |
| Identity | Managed user/workspace provisioning, token revocation or short-lived access tokens, distributed rate limits, role-aware authorization, and audit logs. |

## Runtime Rules

1. API handlers may create commands, runs, and subscriptions, but must not directly own long-running analysis execution.
2. A server restart or replica change must not by itself fail a run. Failure should come from job state, timeout, cancellation, or a terminal worker error.
3. A worker may use local disk as scratch space, but all user-visible or resumable artifacts must have durable object keys.
4. Cleanup must be driven by durable metadata and object storage lifecycle, not by whichever process currently has a session in memory.
5. Production origins must be explicit. Wildcard and localhost origins are development-only.

## Process-Local Assumptions

Development mode is single-process by construction. These are the specific
places where correctness currently depends on that, and what each needs before a
second instance can serve the same workspace:

| Assumption | Where | Why it breaks with two instances | Direction |
|---|---|---|---|
| Session liveness is a map lookup | `session.Manager.IsSessionLive`, `Peek` | A second instance sees the session as not live, so `recoverStaleSessionRuns` marks the first instance's running runs failed | Liveness must come from durable run state and a worker lease, not process memory |
| Model history lives in the session object | `agent.Engine.history` | A run resumed on another instance would start from whatever that instance rebuilt, and the two could diverge | Already partly addressed: history is derived from the persisted event log on cold load, so any instance rebuilds the same history |
| Confirmation receipts are in-process | `agent.Engine.confirmationReceipts` | An approval granted on one instance is unknown to another, so the authorized commit is refused | Fail-closed and acceptable for now; a shared store would be needed for cross-instance approval |
| Run execution is in-process | `RUN_BACKEND=inprocess` | Production readiness already rejects it | Durable run/job backend with a worker lease loop |
| Event fan-out is a process-local broker | `handler.GlobalSSEBroker` | A client attached to instance B does not see events produced by instance A | Durable event log plus a shared subscription channel; SSE stays a subscription surface only |
| Session analysis database is a local file | session-scoped SQLite | Two workers cannot share one session's scratch database | Worker-materialized scratch state rebuilt from a durable snapshot manifest |

The second row is the important one for the agent runtime: because model history
is now derived from the persisted log rather than remembered in memory, the
runtime no longer has to be pinned to the process that started a run. The
remaining rows are the work items in the migration order below.

## Migration Order

1. Keep development mode working, but make production mode fail closed while local backends remain selected.
2. Move metadata repositories from SQLite to PostgreSQL behind the existing repository interfaces.
3. Introduce a durable run/job backend and worker lease loop. SSE remains an event subscription channel only.
4. Add S3-compatible object storage and route uploads, reports, Python artifacts, and exports through it.
5. Replace session SQLite as source of truth with durable snapshot manifests and worker-materialized analysis scratch state.
6. Replace default user bootstrap with managed identity and workspace provisioning.

## Non-Goals

- Do not scale by requiring event-stream clients to remain attached to a specific API process.
- Do not treat shared local volumes as durable multi-tenant storage.
- Do not add hidden workflow prompts to compensate for missing backend state.
- Do not make tools return `next_action` advice instead of facts.
