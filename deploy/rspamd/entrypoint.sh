#!/bin/sh
set -eu

# 解析器不对，Spamhaus / URIBL 会拒绝查询，整条 DNSBL 链路等于没有。
: "${LANQIN_RSPAMD_DNS:=127.0.0.1:53}"
: "${LANQIN_REDIS_ADDR:=}"
if [ -f /etc/rspamd/local.d/options.inc ]; then
	sed -i "s#__LANQIN_RSPAMD_DNS__#${LANQIN_RSPAMD_DNS}#" /etc/rspamd/local.d/options.inc
fi
if [ -n "$LANQIN_REDIS_ADDR" ] && [ -f /etc/rspamd/local.d/redis.conf ]; then
	sed -i "s#__LANQIN_REDIS_ADDR__#${LANQIN_REDIS_ADDR}#" /etc/rspamd/local.d/redis.conf
else
	rm -f /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/classifier-bayes.conf
fi

mkdir -p /run/rspamd /var/lib/rspamd/dkim
if id _rspamd >/dev/null 2>&1; then
	chown -R _rspamd:_rspamd /run/rspamd /var/lib/rspamd 2>/dev/null || true
elif id rspamd >/dev/null 2>&1; then
	chown -R rspamd:rspamd /run/rspamd /var/lib/rspamd 2>/dev/null || true
fi

/usr/local/bin/lanqin-rspamd-sync-dkim --once || true
/usr/local/bin/lanqin-rspamd-sync-dkim &
if id _rspamd >/dev/null 2>&1; then
	exec rspamd -f -u _rspamd -g _rspamd
elif id rspamd >/dev/null 2>&1; then
	exec rspamd -f -u rspamd -g rspamd
fi
exec rspamd -f --insecure
