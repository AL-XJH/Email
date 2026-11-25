#!/usr/bin/env bash
# Shell/cron 整机恢复集；与应用内 age 加密备份无关。
# APP_DIR=/opt/lanqin-email BACKUP_DIR=/root/lanqin-backups CONTAINER=... lanqin-backup
# 完整依赖发现、停容器冷备、快照和 SHA256 校验均由同目录 helper 执行。
set -euo pipefail
umask 077
exec python3 "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/lanqin-backup.py" backup "$@"
