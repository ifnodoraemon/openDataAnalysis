-- +goose Up
alter table source_snapshots
  add column if not exists mode varchar(16) not null default 'imported';

-- +goose Down
alter table source_snapshots drop column if exists mode;
