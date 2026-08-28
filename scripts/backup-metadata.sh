#!/bin/bash
set -euo pipefail

DB_PATH="${METADATA_DB_PATH:-./data/metadata/app.db}"
BACKUP_DIR="${BACKUP_DIR:-./data/backups}"
MAX_BACKUPS="${MAX_BACKUPS:-7}"

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_FILE="$BACKUP_DIR/metadata_${TIMESTAMP}.db"

if [ ! -f "$DB_PATH" ]; then
  echo "未找到元数据数据库：$DB_PATH"
  exit 0
fi

sqlite3 "$DB_PATH" ".backup('$BACKUP_FILE')"

gzip -f "$BACKUP_FILE"

BACKUP_COUNT=$(ls -1 "$BACKUP_DIR"/metadata_*.db.gz 2>/dev/null | wc -l)
if [ "$BACKUP_COUNT" -gt "$MAX_BACKUPS" ]; then
  ls -1t "$BACKUP_DIR"/metadata_*.db.gz | tail -n +$((MAX_BACKUPS + 1)) | xargs rm -f
fi

echo "备份完成：${BACKUP_FILE}.gz"
