-- +goose Up
alter table semantic_confirmations
  add column if not exists confirmation_receipt_id varchar(128) not null default '';

alter table semantic_confirmations
  add column if not exists provenance varchar(64) not null default 'authenticated_request';

create unique index if not exists idx_semantic_confirmations_receipt
  on semantic_confirmations(confirmation_receipt_id)
  where confirmation_receipt_id <> '';

-- +goose Down
drop index if exists idx_semantic_confirmations_receipt;
alter table semantic_confirmations drop column if exists provenance;
alter table semantic_confirmations drop column if exists confirmation_receipt_id;
