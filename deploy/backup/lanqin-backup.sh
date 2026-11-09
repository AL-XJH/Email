#!/usr/bin/env bash
# LanQin Email 每日备份：数据库（SQLite 一致性快照）+ 配置 + 附件 + maildir。
#
# 用法：lanqin-backup.sh            # 正常备份
#       APP_DIR=/opt/x lanqin-backup.sh
#
# 数据库用 sqlite3 的 .backup 而不是直接 cp：.backup 会拿一致性快照，
# 复制的是 WAL 里可能还有未落盘数据的文件时，直接 cp 会拿到半个事务。
set -euo pipefail
# 备份里有管理员口令、DKIM 私钥、OAuth/SMTP 凭据和全部邮件，
# 默认 umask 会让它们变成 0644。
umask 077

APP_DIR="${APP_DIR:-/opt/lanqin-email}"
BACKUP_DIR="${BACKUP_DIR:-/root/lanqin-backups}"
KEEP_DAYS="${KEEP_DAYS:-14}"
CONTAINER="${CONTAINER:-lanqin-email-lanqin-email-1}"

stamp="$(date +%Y%m%d-%H%M%S)"
target="$BACKUP_DIR/$stamp"
mkdir -p "$target"
chmod 700 "$BACKUP_DIR" "$target"

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# 1) 数据库：容器里有 sqlite3，写进挂载目录再取出来。
tmp_db="/data/.lanqin-backup-$$.db"
docker exec "$CONTAINER" sqlite3 /data/lanqin.db ".backup '$tmp_db'"
if ! docker exec "$CONTAINER" sqlite3 "$tmp_db" "PRAGMA quick_check;" | grep -q '^ok$'; then
  docker exec "$CONTAINER" rm -f "$tmp_db" || true
  log "backup failed: quick_check did not return ok"
  exit 1
fi
mv "$APP_DIR${tmp_db}" "$target/lanqin.db"
log "db: $(du -h "$target/lanqin.db" | cut -f1)"

# 2) 配置：.env 里有管理员口令和各项密钥，dkim 里有签名私钥。
if [ -f "$APP_DIR/.env" ]; then
  tar -C "$APP_DIR" -czf "$target/config.tgz" .env 2>/dev/null || true
fi
if [ -d "$APP_DIR/dkim" ]; then
  tar -C "$APP_DIR" -czf "$target/dkim.tgz" dkim
fi
[ -f "$APP_DIR/docker-compose.yml" ] && cp "$APP_DIR/docker-compose.yml" "$target/docker-compose.yml"

# 3) 附件与邮件原文。附件在 data/attachments，maildir 是 Dovecot 的落盘副本。
if [ -d "$APP_DIR/data/attachments" ]; then
  tar -C "$APP_DIR/data" -czf "$target/attachments.tgz" attachments
fi
if [ -d "$APP_DIR/mail" ]; then
  tar -C "$APP_DIR" -czf "$target/mail.tgz" mail
fi

# 3b) Redis：Bayes 学习样本、灰名单、reputation 都在 AOF 里，
#     不备份的话恢复后反垃圾等于从零开始学。
if [ -d "$APP_DIR/redis" ]; then
  docker exec "$CONTAINER" redis-cli BGSAVE >/dev/null 2>&1 || true
  sleep 1
  tar -C "$APP_DIR" -czf "$target/redis.tgz" redis 2>/dev/null || true
fi

# 4) 滚动清理：只删本脚本自己命名格式（YYYYmmdd-HHMMSS）的目录。
find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '20[0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]' -mtime "+$KEEP_DAYS" -exec rm -rf {} + 2>/dev/null || true

total="$(du -sh "$target" | cut -f1)"
count="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '20*' | wc -l)"
log "backup done: $target ($total), kept $count sets"
