# Benchmark and Regression Evaluation

Updated: 2026-08-11

This document defines regression evaluation for the data-analysis agent. The goal is not to imitate a public leaderboard; it is to detect degradation in the product's core capabilities.

## Evaluation goals

The evaluation surface covers:

- Importing CSV, Excel, and SQL source snapshots.
- Inspecting schema, field mappings, time grains, units, and data quality.
- Running read-only SQL and, when selected by the model, Python.
- Exposing join keys, grains, and metric conflicts across tables.
- Delivering charts, findings, limitations, and restorable report state.
- Avoiding premature claims and recovering from tool or delegate failures.

## Scenario corpus

The corpus is stored under `samples/coverage_scenarios/`. Each directory contains one or more CSV files and a `scenario.yaml` file with task facts and observable acceptance criteria. Scenario fixtures may represent concrete domains, but runtime behavior is never selected from their names, industries, headers, or expected phrases.

Generate the human review checklist with:

```bash
python scripts/render_scenario_checklist.py \
  --output samples/coverage_scenarios/CHECKLIST.generated.md
```

## Runners

- `scripts/run_scenario.js` runs one scenario.
- `scripts/eval_report.js` checks report output.
- `scripts/render_scenario_checklist.py` renders a human review checklist.
- `scripts/smoke_test.sh` runs the local smoke suite.

Run scenarios only after the local service is available so service startup failures remain distinct from agent behavior failures.

## Metrics

Primary metrics:

- `load_success`
- `schema_inspected`
- `sql_exec_success`
- `numeric_answer_accuracy`
- `chart_valid`
- `report_finalized`
- `report_reopen_success`

Behavior metrics:

- `did_overclaim`
- `did_join_correctly`
- `did_state_limits`
- `did_ask_user_when_needed`
- `did_recover_from_tool_failure`
- `did_recover_from_delegate_failure`

Source metrics:

- `source_import_success`
- `semantic_profile_created`
- `ambiguity_surfaced`
- `confirmation_applied`
- `import_truncated_reported`

## Suite selection

Suites are selected by declared capability coverage, not by domain keywords or a hardcoded list of preferred scenario IDs. A blocking suite must cover, at minimum, successful single-source analysis, a valid multi-source relationship, unresolved ambiguity, and a recoverable failure. Extended suites add incompatible grains, units, keys, delegation failures, and bounded large-data imports.

## When to run

Run at least the blocking capability suite after changes to:

- Runtime prompts, tool schemas, or tool descriptions.
- SQL, Python, report, or sanitizer tools.
- Data import, semantic profiles, confirmations, or source APIs.
- Session, run, or report restoration.
- Context compaction, delegation, or tracing.

## Evaluation rules

- Trustworthy behavior matters more than a superficially complete report.
- A partial answer is better than an unsupported complete answer.
- Charts are based on queried or structurally persisted data.
- Sampled or truncated data is disclosed in report limitations.
- Tool failures are returned as facts; the model chooses a different valid path instead of a runtime-coded fallback.
- Assertions inspect structured events and state wherever possible. Phrase matching is limited to presentation checks and never defines runtime semantics.
