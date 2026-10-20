-- +goose Up
alter table analysis_runs
  add column if not exists prompt_tokens bigint not null default 0;

alter table analysis_runs
  add column if not exists completion_tokens bigint not null default 0;

-- +goose Down
alter table analysis_runs drop column if exists completion_tokens;
alter table analysis_runs drop column if exists prompt_tokens;
