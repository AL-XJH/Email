#!/bin/sh
# Check API readiness directly: the public HTTP vhost may only redirect to HTTPS.
set -eu
api_status=$(curl --silent --show-error --connect-timeout 1 --max-time 2 \
	--output /dev/null --write-out '%{http_code}' http://127.0.0.1:8080/readyz)
[ "$api_status" = 200 ] || exit 1

# Independently require a responding reverse proxy. Do not follow redirects to
# an external hostname: readiness above has already checked the API itself.
proxy_status=$(curl --silent --show-error --connect-timeout 1 --max-time 1 \
	--output /dev/null --write-out '%{http_code}' http://127.0.0.1/livez)
case "$proxy_status" in
200 | 301 | 302 | 307 | 308) ;;
*) exit 1 ;;
esac

kill -0 "$(cat /var/spool/postfix/pid/master.pid)"
doveadm service status >/dev/null 2>&1
rspamadm control stat >/dev/null 2>&1
[ "$(redis-cli --raw ping 2>/dev/null)" = PONG ]
