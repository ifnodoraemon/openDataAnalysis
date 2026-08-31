-- +goose Up
create table if not exists revoked_tokens (
  jti text primary key,
  expires_at_unix bigint not null
);

-- +goose Down
drop table if exists revoked_tokens;
