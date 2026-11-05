# syntax=docker/dockerfile:1.7

FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive
# Rspamd 走官方仓库（Debian bookworm 自带的是 2022 年的 3.4）。
ARG RSPAMD_REPO=https://rspamd.com/apt-stable
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
    mkdir -p /etc/apt/keyrings && \
    curl -fsSL "$RSPAMD_REPO/gpg.key" | gpg --dearmor > /etc/apt/keyrings/rspamd.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/rspamd.gpg] $RSPAMD_REPO/ bookworm main" > /etc/apt/sources.list.d/rspamd.list && \
    apt-get update && apt-get install -y --no-install-recommends rspamd sqlite3 ca-certificates
COPY local.d/ /etc/rspamd/local.d/
COPY override.d/ /etc/rspamd/override.d/
COPY sync-dkim.sh /usr/local/bin/lanqin-rspamd-sync-dkim
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/lanqin-rspamd-sync-dkim && mkdir -p /run/rspamd /var/lib/rspamd/dkim
EXPOSE 11332 11334
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=6 \
  CMD rspamadm control stat >/dev/null 2>&1 || exit 1
CMD ["/entrypoint.sh"]
