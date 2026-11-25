# syntax=docker/dockerfile:1.7

FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive
ARG DOVECOT_REPO=https://repo.dovecot.org/ce-2.4-latest/debian/trixie
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL "https://repo.dovecot.org/DOVECOT-REPO-GPG-2.4" | gpg --dearmor > /etc/apt/keyrings/dovecot.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/dovecot.gpg] $DOVECOT_REPO trixie main" > /etc/apt/sources.list.d/dovecot.list && \
    apt-get update && apt-get install -y --no-install-recommends dovecot-core dovecot-imapd dovecot-pop3d dovecot-lmtpd dovecot-sieve dovecot-sqlite ssl-cert ca-certificates
COPY dovecot.conf /etc/dovecot/dovecot.conf
COPY dovecot-sql.conf.ext /etc/dovecot/dovecot-sql.conf.ext
COPY sieve/ /etc/dovecot/sieve/
COPY entrypoint.sh /entrypoint.sh
RUN sievec /etc/dovecot/sieve/spam.sieve && \
    chmod 644 /etc/dovecot/sieve/spam.sieve /etc/dovecot/sieve/spam.svbin

RUN chmod +x /entrypoint.sh
EXPOSE 993 995 24 12345
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=6 \
  CMD doveadm service status >/dev/null 2>&1 || exit 1
CMD ["/entrypoint.sh"]

