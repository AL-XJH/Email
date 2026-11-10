# XJH Email

[![English](https://img.shields.io/badge/English-README-blue)](./README.md)
[![简体中文](https://img.shields.io/badge/%E7%AE%80%E4%BD%93%E4%B8%AD%E6%96%87-README-green)](./README.zh-CN.md)

![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go)
![React](https://img.shields.io/badge/React-18.3-61DAFB?logo=react)
![TypeScript](https://img.shields.io/badge/TypeScript-5.6-3178C6?logo=typescript)
![SQLite](https://img.shields.io/badge/SQLite-003B57?logo=sqlite)
![Docker](https://img.shields.io/badge/Docker-2496ED?logo=docker)
![Postfix](https://img.shields.io/badge/Postfix-5E3C2B?logo=maildotru)
![Dovecot](https://img.shields.io/badge/Dovecot-4B8BBE)
![Rspamd](https://img.shields.io/badge/Rspamd-FFD045)

This is **AL-XJH/Email**, a personal self-hosted fork of [LanQin996/LanQin-Email](https://github.com/LanQin996/LanQin-Email).

The frontend is React + TypeScript + shadcn/ui, the backend is Go + SQLite, and production runs as a single all-in-one container with API, Web, Nginx, Postfix, Dovecot, Rspamd and Redis. This fork is not trying to be a generic product: it is shaped around one operator's daily mail workflow, with a denser UI, spam handling, Telegram notifications and external IMAP, plus the data-safety work needed for long-term self-hosting.

Production instance: `https://mail.alxjh.cn`

## What this fork changes

### UI and interaction

- Dense webmail layout: 73px message rows, 36px sidebar rhythm, one 12/8/4/full radius system.
- Mobile swipe-back from reader to list; folder/filter changes return to the top without flashing.
- Collapsible label/folder groups, independently scrolling context menus, keyboard-friendly compact rows.
- Removed the upstream "mailbox transfer" feature; it is not useful for a personal deployment.

### Inbound and spam

- Rspamd scores are routed to Junk at the configured threshold; tunable from the admin panel.
- Sender allowlist, sender blocking, verification-code chips, safe HTML rendering.
- Catch-all mail and normal mailboxes share the same Maildir → SQLite ingestion path.
- Search is pushed down to the server; unread/starred/attachment filters apply before pagination.

### Reading and composing

- Reply prefers `Reply-To`; Reply All is available on desktop and mobile; replying to your own Sent copy does not address yourself.
- Forwarding keeps attachments and formatting; remote IMAP attachments can be forwarded too.
- Draft autosave is serialized and deduplicated by a client token; sending is idempotent, so retries do not send a second copy.
- Scheduled send, single-request bulk operations, attachment count/size limits.
- Translation has a three-level fallback, does not trigger on Chinese text, and HTML anchors no longer open a blank tab.

### Telegram notifications

- One message per mail, bold subject on the first line, recipient/verification-code information on the second.
- Body capped at 1300 characters; long links collapse to a clickable "link"; English mail is translated before pushing.
- Bot tokens are encrypted at rest; the notification outbox has retries and failure records.

### External mailboxes

- Gmail/Outlook OAuth2 and plain IMAP accounts.
- Local-storage mode syncs into SQLite; remote-direct mode reads the remote server without writing local message rows.
- 50MB per-message cap; server-declared literal sizes are not trusted and reads are bounded.
- Private/loopback targets are rejected by default to prevent SSRF.

### Security and reliability

- TOTP 2FA: setup, enable and disable require the current password and share re-auth rate limits; an account with 2FA enabled is never downgraded because a global toggle was turned off.
- RBAC plus delegated-admin dominance: a helpdesk account cannot reset a super admin, delete their mailbox, or reassign it to itself.
- API tokens have independent scopes; mailbox password reset uses `users:reset-password` instead of `mailboxes:write`.
- SMTP/OAuth/Turnstile/external-IMAP secrets use `enc:v1:` and legacy plaintext is migrated at startup.
- SMTP submission has connection/concurrency limits; port 465 wraps the limiter in TLS so `*tls.Conn` is not hidden from go-smtp.
- Maildir renames have rollback and cleanup watermarks; messages without a `Message-ID` get a stable SHA-256 synthetic id so renames do not duplicate them.
- SQLite-only deployment; MySQL/PostgreSQL support and related code were removed.

## Stack

| Layer | Technology |
| --- | --- |
| Frontend | React 18, TypeScript, Vite, shadcn/ui, TanStack Query |
| Backend | Go 1.27, SQLite, Maildir sync, SMTP/IMAP clients |
| Mail stack | Postfix, Dovecot, Rspamd, Redis 8 |
| Deployment | Docker Compose, all-in-one by default (split stack optional) |

## Repository layout

```text
.
├── apps/api              # Go API, SQLite schema, mail sync and business logic
├── apps/web              # React/Vite webmail and admin panel
├── deploy                # Compose, image build, Postfix/Dovecot/Rspamd config
├── docs                  # API, deployment and native-install docs
└── .github/workflows     # image build/release and CI
```

## Requirements

### Development

- Go 1.27+
- Node.js 20+
- pnpm 10.28.2 (corepack works)

### Deployment

- Docker Engine
- Docker Compose v2
- A resolvable mail domain
- Ports 25 / 465 / 587 / 993 / 995 (plus 80/443 for the web UI)

> Public mail also needs correct MX, SPF, DKIM and DMARC records, and a provider that does not block port 25. Many clouds block it by default and require a support request.

## Quick start

### Local development

Backend:

```bash
cd apps/api
go mod download
go test ./...
go run ./cmd/server
```

Frontend (new terminal):

```bash
cd apps/web
corepack enable
corepack prepare pnpm@10.28.2 --activate
pnpm install
pnpm run dev
```

Open:

- Web: `http://localhost:5173`
- API: `http://localhost:8080`

The default admin email is `admin@lanqin.local`. Set `LANQIN_ADMIN_PASSWORD` explicitly for development; otherwise the first startup generates a random password and prints it to the log.

### Docker (single container)

The server only needs the Compose files and config under `deploy/`; no build step on the server:

```bash
cd deploy
cp .env.example .env
# edit domain, public URL, admin email/password, etc.
docker compose pull
docker compose up -d
```

Image:

```text
ghcr.io/al-xjh/lanqin-email:latest
```

You can also pin a release tag such as `ghcr.io/al-xjh/lanqin-email:v1.2.5-lanqin.92`. Back up `.env` before an upgrade; rollback is the previous image plus `docker compose up -d`.

### Build from source

```bash
docker build -f deploy/all-in-one/Dockerfile -t lanqin-email:dense .
docker tag lanqin-email:dense ghcr.io/al-xjh/lanqin-email:latest
```

Common commands:

```bash
docker compose logs -f lanqin-email
docker compose pull && docker compose up -d
curl -sk https://mail.example.com/readyz
```

See [`deploy/README.md`](./deploy/README.md) for more.

## First deployment checklist

1. Set `LANQIN_PUBLIC_HOSTNAME`, `LANQIN_PUBLIC_BASE_URL`, `LANQIN_ADMIN_EMAIL` and `LANQIN_ADMIN_PASSWORD` in `deploy/.env`.
2. Mount real certificates and set `LANQIN_TLS_CERT_FILE` / `LANQIN_TLS_KEY_FILE`.
3. Add the mail domain in the admin panel; configure MX, SPF, DKIM and DMARC, then run the DNS check.
4. Create mailboxes, aliases and permission groups; enable 2FA, Turnstile, invitation/open registration as needed.
5. Send and receive a test message through the admin SMTP test and webmail, then test 465/587 and 993/995 with a third-party client.
6. Configure backups covering `.env`, SQLite, Maildir and DKIM private keys.

## Key environment variables

See [`deploy/.env.example`](./deploy/.env.example) for everything. The common ones:

| Variable | Purpose | Default / example |
| --- | --- | --- |
| `LANQIN_IMAGE` | All-in-one image for this fork | `ghcr.io/al-xjh/lanqin-email:latest` |
| `LANQIN_PUBLIC_HOSTNAME` | Mail server hostname | `mail.example.com` |
| `LANQIN_PUBLIC_BASE_URL` | Public webmail URL | `https://mail.example.com` |
| `LANQIN_ADMIN_EMAIL` | Initial admin email | `admin@example.com` |
| `LANQIN_ADMIN_PASSWORD` | Initial admin password; change in production | `ChangeMe123!` |
| `LANQIN_DB_PATH` | SQLite database path | `/data/lanqin.db` |
| `LANQIN_DATA_DIR` | Data directory (secret, nonce, etc.) | `/data` |
| `LANQIN_ALLOW_INSECURE_HTTP` | Allow non-HTTPS cookies; local debugging only | `false` |
| `LANQIN_TRUSTED_PROXY_COUNT` | Trusted reverse-proxy hops | `1` in the bundled Compose |
| `LANQIN_SMTP_HOST` / `LANQIN_SMTP_PORT` | Outbound SMTP for webmail | `127.0.0.1` / `25` |
| `LANQIN_MAILDIR_ROOT` | Maildir root | `/var/mail/vhosts` |
| `LANQIN_MAILDIR_SCAN_SECONDS` | Maildir scan interval | `5` |
| `LANQIN_RSPAMD_DNS` | DNS used by Rspamd | `127.0.0.11:53` |
| `LANQIN_AUTH_POLICY_SECRET` | Shared secret for `/auth-policy`; generated at `/data/auth-policy-secret` when empty | empty |
| `LANQIN_EXTERNAL_IMAP_ENABLED` | Enable external IMAP | `false` |
| `LANQIN_EXTERNAL_IMAP_MAX_MESSAGE_MB` | Per-message external IMAP cap | `50` |
| `LANQIN_EXTERNAL_IMAP_ALLOW_PRIVATE_HOSTS` / `...ALLOW_INSECURE` | Allow private/loopback targets or plaintext IMAP; both default off | `false` |
| `LANQIN_NOTIFICATION_SECRET_KEY` | Root key for Telegram bot tokens and TOTP seeds | random long string |

Generate the notification key with the operator, for example:

```bash
openssl rand -base64 32
```

It is not stored through the admin UI. Losing or changing it makes saved bot tokens undecryptable and invalidates encrypted TOTP seeds; back it up with `.env`.

## Mail flow

1. **Inbound**: Postfix receives → Rspamd scores/marks → Dovecot writes Maildir → API worker syncs to SQLite → webmail displays.
2. **Outbound**: webmail calls the API → MIME is built → written to Sent → SMTP submits to Postfix or an external SMTP server.
3. **Third-party clients**: SMTP 465/587, IMAP 993, POP3 995; certificates must match `LANQIN_PUBLIC_HOSTNAME`.
4. **External mailboxes**: add Gmail/Outlook or plain IMAP in personal settings; local-storage and remote-direct modes are mutually exclusive per account.

## Open API

Use `/api/open/v1` with scoped API tokens. See [`docs/API.md`](./docs/API.md) and the machine-readable [`docs/openapi.json`](./docs/openapi.json).

Sending supports `Idempotency-Key`; delivery status is available through signed webhooks or delivery-event queries.

## Operations

```bash
# backup script
install -m 755 deploy/backup/lanqin-backup.sh /usr/local/bin/lanqin-backup
lanqin-backup

# health
curl -sk https://mail.example.com/readyz

# logs
docker compose logs --since 10m lanqin-email
```

Backups contain a consistent SQLite snapshot, `.env`, DKIM keys, Maildir and attachments. They default to `/root/lanqin-backups/<timestamp>/` and are kept for 14 days. Before restoring, stop Compose and remove `data/lanqin.db-wal` and `data/lanqin.db-shm`; otherwise the old WAL is replayed onto the restored database.

## Security notes

- Change the default admin password; restrict `.env`, SQLite, Maildir and DKIM private keys to the required users.
- Put the web UI behind Nginx or an edge gateway; expose only 80/443, 25, 465, 587, 993 and 995.
- `/auth-policy` uses the shared secret in `/data/auth-policy-secret` and rejects requests when the secret is empty. Do not publish port 8080.
- The split stack trusts the Docker subnet for Postfix relay from the API. Treat a compromised container as an internal-network compromise.

## Relationship to upstream

- Upstream: `LanQin996/LanQin-Email`; `main` tracks upstream and is only used for syncing.
- This fork: `XJH-Email` and `ui/dense`, containing the UI rework, spam handling, notifications, external IMAP and security hardening.
- Changes are not guaranteed to be merged upstream; deploy this fork from `ghcr.io/al-xjh/lanqin-email`.

## License

Original copyright belongs to LanQin996. Changes in this fork are released under the same [GNU GPL v3 only](./LICENSE). Third-party components retain their own licenses and notices.
