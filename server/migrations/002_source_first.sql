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

create table database_connections (
  source_id varchar(64) primary key references data_sources(id) on delete cascade,
  driver varchar(32) not null,
  host varchar(255) not null,
  port integer not null,
  database_name varchar(255) not null,
  default_schema varchar(255) not null default '',
  ssl_mode varchar(32) not null default 'disable',
  username varchar(255) not null,
  secret_ciphertext bytea not null,
  allowlist_json jsonb not null default '[]',
  last_tested_at timestamptz,
  last_test_status varchar(32) not null default '',
  last_error_message text
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
  profile_mode varchar(32) not null default 'sampled'
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
  profile_status varchar(32) not null default 'draft',
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
