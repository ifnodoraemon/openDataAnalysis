-- +goose Up
create table users (
  id varchar(64) primary key,
  email varchar(255) not null unique,
  password_hash text not null default '',
  name varchar(120) not null,
  avatar_url text not null default '',
  status varchar(32) not null default 'active',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  last_login_at timestamptz
);

create table workspaces (
  id varchar(64) primary key,
  name varchar(120) not null,
  slug varchar(120) not null unique,
  owner_user_id varchar(64) not null references users(id),
  status varchar(32) not null default 'active',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table workspace_members (
  id bigserial primary key,
  workspace_id varchar(64) not null references workspaces(id),
  user_id varchar(64) not null references users(id),
  role varchar(32) not null,
  created_at timestamptz not null default now(),
  unique(workspace_id, user_id)
);

create table sessions (
  id varchar(64) primary key,
  workspace_id varchar(64) not null references workspaces(id),
  user_id varchar(64) not null references users(id),
  title varchar(255) not null default '未命名分析',
  status varchar(32) not null default 'active',
  last_run_id varchar(64),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  last_seen_at timestamptz not null default now()
);

create table files (
  id varchar(64) primary key,
  workspace_id varchar(64) not null references workspaces(id),
  uploaded_by varchar(64) not null references users(id),
  display_name varchar(255) not null,
  purpose varchar(32) not null default 'source',
  content_type varchar(255),
  size_bytes bigint not null,
  storage_provider varchar(64) not null,
  bucket varchar(255),
  storage_key text not null,
  checksum varchar(128),
  status varchar(32) not null default 'uploaded',
  visibility varchar(32) not null default 'private',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create table session_files (
  session_id varchar(64) not null references sessions(id) on delete cascade,
  file_id varchar(64) not null references files(id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (session_id, file_id)
);

create table analysis_runs (
  id varchar(64) primary key,
  session_id varchar(64) not null references sessions(id),
  workspace_id varchar(64) not null references workspaces(id),
  user_id varchar(64) not null references users(id),
  parent_run_id varchar(64),
  run_kind varchar(32) not null default 'root',
  delegate_role text not null default '',
  goal_id varchar(64),
  status varchar(32) not null,
  input_message text not null,
  summary text not null default '',
  error_message text,
  report_file_id varchar(64),
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  foreign key (session_id) references sessions(id) on delete cascade,
  foreign key (parent_run_id) references analysis_runs(id) on delete cascade,
  foreign key (report_file_id) references files(id) on delete set null
);

create table reports (
  id varchar(64) primary key,
  run_id varchar(64) not null unique references analysis_runs(id),
  workspace_id varchar(64) not null references workspaces(id),
  title varchar(255) not null,
  author varchar(255) not null default '',
  html_storage_provider varchar(64) not null,
  html_bucket varchar(255),
  html_storage_key text not null,
  snapshot_json jsonb not null,
  created_at timestamptz not null default now(),
  foreign key (run_id) references analysis_runs(id) on delete cascade
);

create table run_messages (
  id varchar(64) primary key,
  run_id varchar(64) not null references analysis_runs(id) on delete cascade,
  session_id varchar(64) not null references sessions(id) on delete cascade,
  workspace_id varchar(64) not null references workspaces(id) on delete cascade,
  type varchar(64) not null,
  name varchar(255),
  tool_call_id varchar(128),
  content text not null,
  success boolean,
  duration bigint,
  created_at timestamptz not null default now()
);

create index idx_run_messages_run on run_messages(run_id);

-- +goose Down
drop table if exists run_messages;
drop table if exists reports;
drop table if exists analysis_runs;
drop table if exists session_files;
drop table if exists files;
drop table if exists sessions;
drop table if exists workspace_members;
drop table if exists workspaces;
drop table if exists users;
