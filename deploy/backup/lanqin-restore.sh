#!/usr/bin/env bash
# 只恢复本脚本产生的 v2 完整恢复集；必须显式指定空的 --app-dir。
# bash lanqin-restore.sh --backup-dir /root/lanqin-backups/<时间戳> --app-dir /opt/lanqin-email
set -euo pipefail
umask 077
exec python3 "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/lanqin-backup.py" restore "$@"
