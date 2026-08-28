# Agentic Principles

Updated: 2026-08-11

This document defines the boundary of the project's agent runtime. Detailed and durable implementation rules live in `AGENTS.md`; this document records the design principles and trade-offs.

## Goal

The backend is an agent runtime, not a hidden workflow engine.

The runtime is responsible for:

- Exposing goals, tools, state, and thin guardrails.
- Managing context, concurrency, cancellation, persistence, and traces.
- Validating whether final output is structurally deliverable.

The model is responsible for:

- Choosing the next action.
- Deciding whether to explore, ask, delegate, write a report, or finish.
- Asking the user or stating an explicit assumption when facts are ambiguous.

## Principles

### 1. Do not prescribe a path

The system may define goals and boundaries, but it must not encode fixed sequences such as `analyze -> write -> finalize` in prompts, tool descriptions, or handler-assembled messages.

### 2. State tools expose facts only

`state_*` tools report current facts such as data sources, semantic profiles, working memory, goals, report structure, and time context. They do not tell the model what to do next.

### 3. Judgment belongs to the model

The runtime does not decide whether delegation is needed, evidence is sufficient, one report section has priority, or the user should be asked a question. The model makes those decisions after inspecting facts.

### 4. Tool contracts are detailed but non-prescriptive

A tool description may define purpose, inputs, outputs, side effects, limits, and failure conditions. It must not hide a workflow or return `next_action` advice. Human-readable display text belongs in `ui_summary`; model-relevant facts remain in separate structured fields. Do not introduce semantically mixed fields such as `summary_text`.

### 5. Guardrails block invalid results

Valid hard constraints include blocking unsafe execution, invalid SQL, broken state, references to missing charts or sources, structurally invalid report finalization, and report previews that bypass sanitizer or CSP boundaries. Guardrails do not plan normal work.

### 6. Finalization is a delivery boundary

After `report_finalize` returns `ok=true`, `is_finalized=true`, and `delivery_state=finalized`, the current run enters completion. The runtime may allow one tools-disabled continuation over the existing history. It must not replace the model's final response with a hardcoded template.

### 7. Ambiguity is explicit

When several metric definitions, join keys, time grains, units, or field mappings remain plausible, the runtime exposes the candidates as facts. The model asks the user or, when authorized, proceeds with an explicitly stated assumption.

### 8. State access is pull-based

Large file summaries, report state, working memory, and history digests are not injected into every turn. The model uses observation tools when it needs those facts.

### 9. Structured state is scaffolding

Working memory, goal trees, and report block trees improve observability and recovery. They do not create a mandatory planning phase or a fixed report outline.

### 10. Report scripts and model content are separate

The model authors report content and structured chart option data. ECharts loaders and chart runtime code use trusted external scripts, preferably same-origin `/assets/echarts.min.js` and `/oda-chart-runtime.js`. Chart support is not a reason to permit broad inline script execution.

### 11. Language boundaries are explicit

User-facing UI, HTTP/SSE status text, `ui_summary`, and operator CLI output use Simplified Chinese. Model-facing policy and tool contracts use English. Machine fields, enum values, protocols, product names, and library names keep their canonical spelling. English and Chinese documentation use separate `.md` and `.zh-CN.md` files.

## Prompt layering

The runtime uses four layers:

1. `system`: static policy and tool boundaries.
2. `user`: the user's direct task.
3. `runtime`: ephemeral facts for the current turn, such as edit scope or active goals.
4. `history`: original conversation and tool results still present in the context window.

Window trimming exposes only factual omission counts in the `runtime` layer; the runtime does not invent semantic history summaries. Decisions that must survive the context window are written explicitly by the model to working memory. Temporary facts, history, and user preferences never move into `system`. Delegate constraints use `policy_appendix`, not context dumps.

## Current anti-patterns

- Linear workflow prompts.
- Tool results that recommend a next action.
- Handler-generated instructions that tell the model which tool to call first.
- Low-confidence candidates presented as verified facts.
- Moving judgment into code because a model sometimes makes a mistake.
- Automatically injecting large state payloads into every turn.
