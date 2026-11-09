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
#    sqlite3 的 .backup 在源文件不存在时会「成功」创建一个空库，
#    所以必须先确认源存在，再确认备份里真的有表。
tmp_db="/data/.lanqin-backup-$$.db"
# 任何中途失败都要删掉容器里的半成品快照，避免 0644 的库副本留在 /data。
trap 'docker exec "$CONTAINER" rm -f "$tmp_db" 2>/dev/null || true' EXIT
if ! docker exec "$CONTAINER" test -f /data/lanqin.db; then
	log "backup failed: /data/lanqin.db not found in $CONTAINER"
	exit 1
fi
docker exec "$CONTAINER" sqlite3 /data/lanqin.db ".backup '$tmp_db'"
docker exec "$CONTAINER" chmod 600 "$tmp_db"
if ! docker exec "$CONTAINER" sqlite3 "$tmp_db" "PRAGMA quick_check;" | grep -q '^ok$'; then
	docker exec "$CONTAINER" rm -f "$tmp_db" || true
	log "backup failed: quick_check did not return ok"
	exit 1
fi
if [ "$(docker exec "$CONTAINER" sqlite3 "$tmp_db" "SELECT COUNT(*) FROM sqlite_master WHERE type='table';")" -lt 1 ]; then
	docker exec "$CONTAINER" rm -f "$tmp_db" || true
	log "backup failed: snapshot has no tables"
	exit 1
fi
mv "$APP_DIR${tmp_db}" "$target/lanqin.db"
chmod 600 "$target/lanqin.db"
log "db: $(du -h "$target/lanqin.db" | cut -f1)"

# 2) 配置：.env 里有管理员口令和各项密钥，dkim 里有签名私钥。
if [ -f "$APP_DIR/.env" ]; then
	# 不能吞掉 tar 的错误：备份里少了 .env，恢复后密钥全变、会话全失效。
	tar -C "$APP_DIR" -czf "$target/config.tgz" .env
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

# 3b) Redis：Bayes 学习样本、灰名单、reputation。
#     不能直接 tar appendonlydir —— redis 正在往里追加，tar 出来的是撕裂的 AOF。
#     BGSAVE 生成一致性 RDB，等它真的结束（rdb_bgsave_in_progress:0）再拷贝。
# Redis 的 dir 是 /data/redis（落在 data 卷里），不是 $APP_DIR/redis；
# 只找后者会让整段 Redis 备份永远跳过。两种布局都支持，可用 REDIS_DIR 覆盖。
REDIS_DIR="${REDIS_DIR:-}"
if [ -z "$REDIS_DIR" ]; then
	if [ -d "$APP_DIR/data/redis" ]; then
		REDIS_DIR="$APP_DIR/data/redis"
	else
		REDIS_DIR="$APP_DIR/redis"
	fi
fi
if [ -d "$REDIS_DIR" ]; then
	docker exec "$CONTAINER" redis-cli BGSAVE >/dev/null 2>&1 || true
	redis_info=""
	for _ in $(seq 1 30); do
		redis_info="$(docker exec "$CONTAINER" redis-cli INFO persistence 2>/dev/null || true)"
		printf '%s' "$redis_info" | grep -q 'rdb_bgsave_in_progress:0' && break
		sleep 1
	done
	if printf '%s' "$redis_info" | grep -q 'rdb_last_bgsave_status:ok' && [ -f "$REDIS_DIR/dump.rdb" ]; then
		cp "$REDIS_DIR/dump.rdb" "$target/redis-dump.rdb"
	else
		log "warning: redis BGSAVE did not finish; redis state not backed up"
	fi
fi

# 4) 滚动清理：只删本脚本自己命名格式（YYYYmmdd-HHMMSS）的目录。
find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '20[0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]' -mtime "+$KEEP_DAYS" -exec rm -rf {} + 2>/dev/null || true

# 备份集要能校验：恢复前先 sha256sum -c，避免拿到半截 tar。
( cd "$target" && sha256sum ./* 2>/dev/null | grep -v 'SHA256SUMS' > SHA256SUMS ) || true

total="$(du -sh "$target" | cut -f1)"
count="$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '20*' | wc -l)"
log "backup done: $target ($total), kept $count sets"
