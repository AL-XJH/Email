# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm AS api-build
WORKDIR /src/apps/api
COPY apps/api/go.mod apps/api/go.sum ./
# Keep modules in the layer: registry cache does not persist cache mounts.
RUN go mod download
COPY apps/api ./
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/lanqin-api ./cmd/server
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/lanqin-backup ./cmd/backup

FROM debian:trixie-slim AS sqlite-build
ARG SQLITE_VERSION=3540100
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates curl && \
    curl -fsSL "https://www.sqlite.org/2026/sqlite-autoconf-${SQLITE_VERSION}.tar.gz" -o /tmp/sqlite.tar.gz && \
    tar -xzf /tmp/sqlite.tar.gz -C /tmp && \
    cd "/tmp/sqlite-autoconf-${SQLITE_VERSION}" && \
    ./configure --prefix=/usr/local --enable-fts5 && \
    make -j"$(nproc)" && \
    make install && \
    rm -rf /tmp/sqlite.tar.gz "/tmp/sqlite-autoconf-${SQLITE_VERSION}"

FROM --platform=$BUILDPLATFORM node:26-bookworm-slim AS web-build
WORKDIR /src
COPY pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json apps/web/package.json
RUN npm install --global pnpm@12.23.0
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --filter lanqin-email-web...
COPY apps/web apps/web
ARG VITE_APP_VERSION=""
ARG VITE_RELEASE_URL=""
RUN pnpm --dir apps/web run build

FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive
# Rspamd 从官方仓库装；trixie 套件里是当前 4.x。
ARG RSPAMD_REPO=https://rspamd.com/apt-stable
# 单容器模式把 Redis 也塞进同一个镜像，省掉一个 sidecar 容器。
ARG REDIS_REPO=https://packages.redis.io/deb
ARG NGINX_REPO=https://nginx.org/packages/mainline/debian
ARG DOVECOT_REPO=https://repo.dovecot.org/ce-2.4-latest/debian/trixie
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
  --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
  rm -f /etc/apt/apt.conf.d/docker-clean && \
  apt-get update && \
  apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
  mkdir -p /etc/apt/keyrings && \
  curl -fsSL "$RSPAMD_REPO/gpg.key" | gpg --dearmor > /etc/apt/keyrings/rspamd.gpg && \
  echo "deb [signed-by=/etc/apt/keyrings/rspamd.gpg] $RSPAMD_REPO/ trixie main" > /etc/apt/sources.list.d/rspamd.list && \
  curl -fsSL "https://packages.redis.io/gpg" | gpg --dearmor > /etc/apt/keyrings/redis.gpg && \
  echo "deb [signed-by=/etc/apt/keyrings/redis.gpg] $REDIS_REPO trixie main" > /etc/apt/sources.list.d/redis.list && \
  curl -fsSL "https://nginx.org/keys/nginx_signing.key" | gpg --dearmor > /etc/apt/keyrings/nginx.gpg && \
  echo "deb [signed-by=/etc/apt/keyrings/nginx.gpg] $NGINX_REPO trixie nginx" > /etc/apt/sources.list.d/nginx.list && \
  curl -fsSL "https://repo.dovecot.org/DOVECOT-REPO-GPG-2.4" | gpg --dearmor > /etc/apt/keyrings/dovecot.gpg && \
  echo "deb [signed-by=/etc/apt/keyrings/dovecot.gpg] $DOVECOT_REPO trixie main" > /etc/apt/sources.list.d/dovecot.list && \
  apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata curl sqlite3 supervisor nginx \
    postfix postfix-sqlite \
    dovecot-core dovecot-imapd dovecot-pop3d dovecot-lmtpd dovecot-sieve dovecot-sqlite ssl-cert \
    rspamd \
    redis-server redis-tools unbound dns-root-data

COPY --from=api-build /out/lanqin-api /usr/local/bin/lanqin-api
COPY --from=api-build /out/lanqin-backup /usr/local/bin/lanqin-backup
COPY --from=sqlite-build /usr/local/bin/sqlite3 /usr/local/bin/sqlite3
COPY --from=web-build /src/apps/web/dist /usr/share/nginx/html

COPY deploy/all-in-one/supervisord.conf /etc/supervisor/conf.d/lanqin.conf
COPY deploy/all-in-one/nginx.conf /etc/nginx/conf.d/default.conf
COPY deploy/all-in-one/redis.conf /etc/redis-lanqin.conf
COPY deploy/all-in-one/unbound.conf /etc/unbound/lanqin.conf
COPY deploy/all-in-one/dns-check.py /usr/local/bin/lanqin-dns-check
COPY deploy/all-in-one/entrypoint.sh /entrypoint.sh
COPY deploy/all-in-one/healthcheck.sh /usr/local/bin/lanqin-healthcheck

COPY deploy/postfix/main.cf /etc/postfix/main.cf
COPY deploy/postfix/master.cf /etc/postfix/master.cf
COPY deploy/postfix/sqlite-*.cf /etc/postfix/
COPY deploy/postfix/configure-db.sh /usr/local/bin/lanqin-postfix-configure-db
COPY deploy/dovecot/dovecot.conf /etc/dovecot/dovecot.conf
COPY deploy/dovecot/dovecot-sql.conf.ext /etc/dovecot/dovecot-sql.conf.ext
COPY deploy/dovecot/configure-db.sh /usr/local/bin/lanqin-dovecot-configure-db
COPY deploy/rspamd/local.d/ /etc/rspamd/local.d/
COPY deploy/rspamd/override.d/ /etc/rspamd/override.d/
COPY deploy/dovecot/sieve/ /etc/dovecot/sieve/
COPY deploy/rspamd/sync-dkim.sh /usr/local/bin/lanqin-rspamd-sync-dkim

RUN sievec /etc/dovecot/sieve/spam.sieve && \
    chmod 644 /etc/dovecot/sieve/spam.sieve /etc/dovecot/sieve/spam.svbin

RUN chmod +x /entrypoint.sh /usr/local/bin/lanqin-healthcheck /usr/local/bin/lanqin-dns-check /usr/local/bin/lanqin-rspamd-sync-dkim /usr/local/bin/lanqin-postfix-configure-db /usr/local/bin/lanqin-dovecot-configure-db && mkdir -p /run/nginx /run/rspamd /var/mail/vhosts /data /var/lib/rspamd/dkim

EXPOSE 80 443 25 465 587 993 995
HEALTHCHECK --interval=10s --timeout=8s --start-period=30s --retries=6 \
  CMD ["/usr/local/bin/lanqin-healthcheck"]
CMD ["/entrypoint.sh"]
