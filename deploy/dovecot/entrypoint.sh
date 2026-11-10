#!/bin/sh
set -eu
: "${LANQIN_TLS_CERT_FILE:=}"
: "${LANQIN_TLS_KEY_FILE:=}"
# The bundled dovecot.conf ships the all-in-one address, where the API shares this
# container. In the split stack the API is a separate service, so 127.0.0.1 would
# point at Dovecot itself and the IMAP/POP3 rate limits would silently never apply.
: "${LANQIN_AUTH_POLICY_URL:=http://127.0.0.1:8080/auth-policy}"
addgroup --system --gid 5000 vmail 2>/dev/null || true
adduser --system --uid 5000 --gid 5000 --home /var/mail/vhosts --no-create-home vmail 2>/dev/null || true
mkdir -p /data /var/mail/vhosts
chown -R 5000:5000 /var/mail/vhosts
AUTH_POLICY_NONCE_FILE="${LANQIN_AUTH_POLICY_NONCE_FILE:-/data/dovecot-auth-policy-nonce}"
mkdir -p "$(dirname "$AUTH_POLICY_NONCE_FILE")"
if [ ! -s "$AUTH_POLICY_NONCE_FILE" ]; then
	od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >"$AUTH_POLICY_NONCE_FILE"
fi
chmod 600 "$AUTH_POLICY_NONCE_FILE" 2>/dev/null || true
AUTH_POLICY_HASH_NONCE="$(cat "$AUTH_POLICY_NONCE_FILE")"
AUTH_POLICY_SECRET_FILE="${LANQIN_AUTH_POLICY_SECRET_FILE:-/data/auth-policy-secret}"
if [ -n "${LANQIN_AUTH_POLICY_SECRET:-}" ]; then
	AUTH_POLICY_SECRET="$LANQIN_AUTH_POLICY_SECRET"
elif [ -s "$AUTH_POLICY_SECRET_FILE" ]; then
	AUTH_POLICY_SECRET="$(cat "$AUTH_POLICY_SECRET_FILE")"
else
	mkdir -p "$(dirname "$AUTH_POLICY_SECRET_FILE")"
	od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >"$AUTH_POLICY_SECRET_FILE"
	chmod 600 "$AUTH_POLICY_SECRET_FILE" 2>/dev/null || true
	AUTH_POLICY_SECRET="$(cat "$AUTH_POLICY_SECRET_FILE")"
fi
export LANQIN_AUTH_POLICY_SECRET="$AUTH_POLICY_SECRET"
AUTH_POLICY_URL="${LANQIN_AUTH_POLICY_URL:-http://127.0.0.1:8080/auth-policy}"
case "$AUTH_POLICY_URL" in
	*\?*) AUTH_POLICY_SEP='&' ;;
	*) AUTH_POLICY_SEP='?' ;;
esac
AUTH_POLICY_SECRET_SED="$(printf '%s' "$AUTH_POLICY_SECRET" | sed 's/[&#]/\\&/g')"
TLS_CERT=/etc/ssl/certs/ssl-cert-snakeoil.pem
TLS_KEY=/etc/ssl/private/ssl-cert-snakeoil.key
if [ -n "$LANQIN_TLS_CERT_FILE" ] || [ -n "$LANQIN_TLS_KEY_FILE" ]; then
	if [ -f "$LANQIN_TLS_CERT_FILE" ] && [ -f "$LANQIN_TLS_KEY_FILE" ]; then
		TLS_CERT="$LANQIN_TLS_CERT_FILE"
		TLS_KEY="$LANQIN_TLS_KEY_FILE"
	else
		echo "warning: LANQIN_TLS_CERT_FILE/LANQIN_TLS_KEY_FILE not readable; using snakeoil localhost certificate" >&2
	fi
fi
sed -i "s#^ssl_cert = <.*#ssl_cert = <${TLS_CERT}#" /etc/dovecot/dovecot.conf
sed -i "s#^ssl_key = <.*#ssl_key = <${TLS_KEY}#" /etc/dovecot/dovecot.conf
sed -i "s#^auth_policy_hash_nonce = .*#auth_policy_hash_nonce = ${AUTH_POLICY_HASH_NONCE}#" /etc/dovecot/dovecot.conf
sed -i "s#^auth_policy_server_url = .*#auth_policy_server_url = ${AUTH_POLICY_URL}${AUTH_POLICY_SEP}key=${AUTH_POLICY_SECRET_SED}#" /etc/dovecot/dovecot.conf
# Printed so an operator can confirm the address is right for their topology: a
# wrong one does not fail loudly, it just stops enforcing IMAP/POP3 rate limits.
echo "dovecot: auth policy server = ${AUTH_POLICY_URL} (shared secret configured)" >&2
exec dovecot -F
