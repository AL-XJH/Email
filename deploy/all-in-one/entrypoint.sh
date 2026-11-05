#!/bin/sh
set -eu

: "${LANQIN_PUBLIC_HOSTNAME:=mail.example.com}"
: "${LANQIN_DATA_DIR:=/data}"
: "${LANQIN_DB_PATH:=/data/lanqin.db}"
: "${LANQIN_DB_DRIVER:=sqlite}"
: "${LANQIN_ADDR:=127.0.0.1:8080}"
: "${LANQIN_SMTP_HOST:=127.0.0.1}"
: "${LANQIN_SMTP_PORT:=25}"
: "${LANQIN_SUBMISSION_ADDR:=}"
: "${LANQIN_SUBMISSION_TLS_ADDR:=}"
: "${LANQIN_SUBMISSION_MAX_MESSAGE_MB:=35}"
: "${LANQIN_MAILDIR_ROOT:=/var/mail/vhosts}"
: "${LANQIN_TLS_CERT_FILE:=}"
: "${LANQIN_TLS_KEY_FILE:=}"
# Rspamd 的 DNS 解析器：必须是本地递归解析器，公共 DNS 会让 DNSBL 全部失效。
: "${LANQIN_RSPAMD_DNS:=127.0.0.1:53}"
# Rspamd 的后端存储（Bayes / 灰名单 / ratelimit）。留空则这些模块保持禁用。
: "${LANQIN_REDIS_ADDR:=}"

export LANQIN_RSPAMD_DNS LANQIN_REDIS_ADDR
export LANQIN_DATA_DIR LANQIN_DB_PATH LANQIN_DB_DRIVER LANQIN_ADDR LANQIN_SMTP_HOST LANQIN_SMTP_PORT LANQIN_SUBMISSION_ADDR LANQIN_SUBMISSION_TLS_ADDR LANQIN_SUBMISSION_MAX_MESSAGE_MB LANQIN_MAILDIR_ROOT LANQIN_TLS_CERT_FILE LANQIN_TLS_KEY_FILE

addgroup --system --gid 5000 vmail 2>/dev/null || true
adduser --system --uid 5000 --gid 5000 --home /var/mail/vhosts --no-create-home vmail 2>/dev/null || true
mkdir -p /data /var/mail/vhosts /var/lib/rspamd/dkim /run/rspamd /var/spool/postfix /var/run/dovecot
chown -R 5000:5000 /var/mail/vhosts
if id _rspamd >/dev/null 2>&1; then
	chown -R _rspamd:_rspamd /run/rspamd /var/lib/rspamd 2>/dev/null || true
elif id rspamd >/dev/null 2>&1; then
	chown -R rspamd:rspamd /run/rspamd /var/lib/rspamd 2>/dev/null || true
fi

AUTH_POLICY_NONCE_FILE="${LANQIN_AUTH_POLICY_NONCE_FILE:-/data/dovecot-auth-policy-nonce}"
mkdir -p "$(dirname "$AUTH_POLICY_NONCE_FILE")"
if [ ! -s "$AUTH_POLICY_NONCE_FILE" ]; then
	od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >"$AUTH_POLICY_NONCE_FILE"
fi
chmod 600 "$AUTH_POLICY_NONCE_FILE" 2>/dev/null || true
AUTH_POLICY_HASH_NONCE="$(cat "$AUTH_POLICY_NONCE_FILE")"

/usr/local/bin/lanqin-postfix-configure-db
/usr/local/bin/lanqin-dovecot-configure-db

TLS_CERT=/etc/ssl/certs/ssl-cert-snakeoil.pem
TLS_KEY=/etc/ssl/private/ssl-cert-snakeoil.key
if [ -n "$LANQIN_TLS_CERT_FILE" ] || [ -n "$LANQIN_TLS_KEY_FILE" ]; then
	if [ -f "$LANQIN_TLS_CERT_FILE" ] && [ -f "$LANQIN_TLS_KEY_FILE" ]; then
		TLS_CERT="$LANQIN_TLS_CERT_FILE"
		TLS_KEY="$LANQIN_TLS_KEY_FILE"
		: "${LANQIN_SUBMISSION_ADDR:=:587}"
		: "${LANQIN_SUBMISSION_TLS_ADDR:=:465}"
	else
		echo "warning: LANQIN_TLS_CERT_FILE/LANQIN_TLS_KEY_FILE not readable; using snakeoil localhost certificate" >&2
	fi
fi
if [ -n "$LANQIN_SUBMISSION_ADDR$LANQIN_SUBMISSION_TLS_ADDR" ] && { [ "$TLS_CERT" = "/etc/ssl/certs/ssl-cert-snakeoil.pem" ] || [ "$TLS_KEY" = "/etc/ssl/private/ssl-cert-snakeoil.key" ]; }; then
	echo "warning: SMTP submission disabled because LANQIN_TLS_CERT_FILE/LANQIN_TLS_KEY_FILE are not configured with readable certificate files" >&2
	LANQIN_SUBMISSION_ADDR=""
	LANQIN_SUBMISSION_TLS_ADDR=""
fi
export LANQIN_SUBMISSION_ADDR LANQIN_SUBMISSION_TLS_ADDR

postconf -e "myhostname = ${LANQIN_PUBLIC_HOSTNAME}"
postconf -e "myorigin = ${LANQIN_PUBLIC_HOSTNAME}"
postconf -e "smtpd_tls_cert_file = ${TLS_CERT}"
postconf -e "smtpd_tls_key_file = ${TLS_KEY}"
postconf -e "virtual_transport = lmtp:inet:127.0.0.1:24"
postconf -e "milter_mail_macros = i {mail_addr} {client_addr} {client_name} {auth_authen}"
postconf -e "smtpd_milters = inet:127.0.0.1:11332"
postconf -e "non_smtpd_milters = inet:127.0.0.1:11332"
sed -i "s#^ssl_cert = <.*#ssl_cert = <${TLS_CERT}#" /etc/dovecot/dovecot.conf
sed -i "s#^ssl_key = <.*#ssl_key = <${TLS_KEY}#" /etc/dovecot/dovecot.conf
sed -i "s#^auth_policy_hash_nonce = .*#auth_policy_hash_nonce = ${AUTH_POLICY_HASH_NONCE}#" /etc/dovecot/dovecot.conf
if [ -f /etc/rspamd/local.d/options.inc ]; then
	sed -i "s#__LANQIN_RSPAMD_DNS__#${LANQIN_RSPAMD_DNS}#" /etc/rspamd/local.d/options.inc
fi
if [ -n "$LANQIN_REDIS_ADDR" ] && [ -f /etc/rspamd/local.d/redis.conf ]; then
	sed -i "s#__LANQIN_REDIS_ADDR__#${LANQIN_REDIS_ADDR}#" /etc/rspamd/local.d/redis.conf
else
	# 没有配 Redis 就把这两份配置摘掉，避免 rspamd 启动时刷一堆 init 失败日志
	rm -f /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/classifier-bayes.conf
fi

# Rspamd DKIM keys are exported after API seed/migrations are complete.
/usr/local/bin/lanqin-api >/tmp/lanqin-api-bootstrap.log 2>&1 &
bootstrap_pid=$!
bootstrap_ready=0
for i in $(seq 1 60); do
	if curl --fail --silent --show-error http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
		bootstrap_ready=1
		break
	fi
	sleep 1
done
if [ "$bootstrap_ready" -ne 1 ]; then
	echo "error: LanQin API database bootstrap did not become ready" >&2
	cat /tmp/lanqin-api-bootstrap.log >&2 || true
	kill "$bootstrap_pid" 2>/dev/null || true
	wait "$bootstrap_pid" 2>/dev/null || true
	exit 1
fi
kill "$bootstrap_pid" 2>/dev/null || true
wait "$bootstrap_pid" 2>/dev/null || true

/usr/local/bin/lanqin-rspamd-sync-dkim --once || true

postfix check
exec /usr/bin/supervisord -c /etc/supervisor/conf.d/lanqin.conf
