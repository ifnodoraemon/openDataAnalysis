-- +goose Up
create table data_sources (
  id varchar(64) primary key,
  workspace_id varchar(64) not null references workspaces(id),
  name varchar(255) not null,
  source_type varchar(64) not null,
  status varchar(32) not null default 'active',
  file_id varchar(64) references files(id),
  created_by varchar(64) not null references users(id),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index idx_data_sources_workspace on data_sources(workspace_id);

create table source_configs (
  source_id varchar(64) primary key references data_sources(id) on delete cascade,
  connector_type varchar(64) not null,
  config_json jsonb not null default '{}',
  credential_ciphertext bytea not null default ''::bytea,
  last_tested_at timestamptz,
  last_test_status varchar(32) not null default '',
  last_error_message text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table source_snapshots (
  id varchar(64) primary key,
  session_id varchar(64) not null references sessions(id) on delete cascade,
  source_id varchar(64) not null references data_sources(id) on delete cascade,
  upstream_kind varchar(64) not null,
  upstream_schema varchar(255) not null default '',
  upstream_object varchar(255) not null default '',
  analysis_table_name varchar(255) not null,
  row_count integer not null default 0,
  column_count integer not null default 0,
  status varchar(32) not null default 'creating',
  error_message text,
  schema_signature varchar(64) not null default '',
  imported_at timestamptz not null default now(),
  rows_imported integer not null default 0,
  rows_skipped integer not null default 0,
  import_row_limit integer not null default 0,
  import_truncated boolean not null default false,
  import_duration_ms integer not null default 0,
  profile_duration_ms integer not null default 0,
  snapshot_size_bytes bigint not null default 0,
  profile_mode varchar(32) not null default 'pending'
);

create index idx_source_snapshots_session on source_snapshots(session_id);
create index idx_source_snapshots_source on source_snapshots(source_id);

create table session_source_bindings (
  session_id varchar(64) not null references sessions(id) on delete cascade,
  source_id varchar(64) not null references data_sources(id) on delete cascade,
  source_object_key varchar(255) not null,
  active_snapshot_id varchar(64) not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (session_id, source_id, source_object_key)
);

create table semantic_profiles (
  id varchar(64) primary key,
  session_id varchar(64) not null,
  source_id varchar(64) not null references data_sources(id) on delete cascade,
  snapshot_id varchar(64) not null references source_snapshots(id) on delete cascade,
  analysis_table_name varchar(255) not null,
  schema_signature varchar(64) not null default '',
  profile_status varchar(32) not null default 'profiled',
  profile_json jsonb not null default '{}',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index idx_semantic_profiles_session on semantic_profiles(session_id);
create index idx_semantic_profiles_source on semantic_profiles(source_id);

create table semantic_confirmations (
  id varchar(64) primary key,
  profile_id varchar(64) not null references semantic_profiles(id) on delete cascade,
  workspace_id varchar(64) not null,
  session_id varchar(64) not null,
  confirmed_by varchar(64) not null,
  scope varchar(32) not null default 'session',
  overrides_json jsonb not null default '{}',
  created_at timestamptz not null default now()
);

create index idx_semantic_confirmations_profile on semantic_confirmations(profile_id);
create index idx_semantic_confirmations_workspace on semantic_confirmations(workspace_id);

create table semantic_assets (
  id varchar(64) primary key,
  workspace_id varchar(64) not null references workspaces(id),
  source_id varchar(64) not null default '',
  schema_signature varchar(64) not null default '',
  asset_kind varchar(64) not null,
  asset_key varchar(255) not null,
  asset_value_json jsonb not null default '{}',
  created_from_profile_id varchar(64) not null default '',
  created_from_confirmation_id varchar(64) not null default '',
  created_by varchar(64) not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique(workspace_id, schema_signature, asset_kind, asset_key)
);

create index idx_semantic_assets_workspace on semantic_assets(workspace_id);
create index idx_semantic_assets_schema on semantic_assets(workspace_id, schema_signature);

create table audit_events (
  id varchar(64) primary key,
  workspace_id varchar(64) not null references workspaces(id),
  session_id varchar(64) not null default '',
  run_id varchar(64) not null default '',
  actor_user_id varchar(64) not null default '',
  event_type varchar(128) not null,
  resource_type varchar(64) not null,
  resource_id varchar(128) not null,
  payload_json jsonb not null default '{}',
  created_at timestamptz not null default now()
);

create index idx_audit_events_workspace on audit_events(workspace_id, created_at desc);
create index idx_audit_events_resource on audit_events(resource_type, resource_id);

-- +goose Down
drop table if exists audit_events;
drop table if exists semantic_assets;
drop table if exists semantic_confirmations;
drop table if exists semantic_profiles;
drop table if exists session_source_bindings;
drop table if exists source_snapshots;
drop table if exists source_configs;
drop table if exists data_sources;
