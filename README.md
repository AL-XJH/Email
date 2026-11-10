# XJH 邮箱

![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go)
![React](https://img.shields.io/badge/React-18.3-61DAFB?logo=react)
![TypeScript](https://img.shields.io/badge/TypeScript-5.6-3178C6?logo=typescript)
![SQLite](https://img.shields.io/badge/SQLite-003B57?logo=sqlite)
![Docker](https://img.shields.io/badge/Docker-2496ED?logo=docker)
![Postfix](https://img.shields.io/badge/Postfix-5E3C2B?logo=maildotru)
![Dovecot](https://img.shields.io/badge/Dovecot-4B8BBE)
![Rspamd](https://img.shields.io/badge/Rspamd-FFD045)

这是 **AL-XJH/Email**，一个以 [LanQin996/LanQin-Email](https://github.com/LanQin996/LanQin-Email) 为基础的个人自建邮箱分支。

前端是 React + TypeScript + shadcn/ui，后端是 Go + SQLite；生产部署使用单容器，把 API、Web、Nginx、Postfix、Dovecot、Rspamd 和 Redis 放在一起。这个分支不再追求“功能大而全”，而是按实际使用场景重排界面、补反垃圾/通知/外部邮箱，并把安全与数据可靠性做到能长期自用。


## 这个分支做了什么

### 界面与交互

- 按 seek.li 的密度重排 网页邮箱：列表行高 73px、侧栏 36px 节奏、统一 12/8/4/full 四档圆角。
- 手机上从阅读页侧滑返回列表；切换文件夹/筛选自动回到列表顶部，刷新不闪。
- 标签/文件夹分组折叠、右键菜单独立滚动、紧凑模式支持键盘操作。
- 删除「邮箱转移」：个人使用不需要把邮箱转给别人。

### 收信与反垃圾

- Rspamd 评分达到阈值后直接进垃圾箱，可在后台调整。
- 发件人白名单、发件人拦截、验证码识别 chip、HTML 邮件安全渲染。
- catch-all 邮件与普通邮箱统一走 Maildir → SQLite 同步。
- 邮件搜索下推到服务端，未读/星标/附件过滤在分页前生效。

### 阅读与写信

- 回复优先使用 `Reply-To`，支持全部回复，回复自己 Sent 里的邮件不会发给自己。
- 转发保留原附件与排版；远程 IMAP 附件也能直接转发。
- 草稿自动保存串行化，超时重试/双击不会产生两份草稿；发信支持幂等键，重试不会重复投递。
- 定时发送、批量操作单请求化、附件数量/大小前端限制。
- 翻译三级降级：主接口失败自动换备用接口；中文内容不误弹翻译；HTML 锚点不再打开空白页。

### Telegram 通知

- 一封邮件一条消息，第一行加粗主题，第二行收件人/验证码信息。
- 正文最多 1300 字，超长链接收成可点击「链接」，英文邮件自动翻译后再推送。
- Bot Token 加密保存；通知 outbox 有重试和失败记录。

### 外部邮箱

- Gmail / Outlook OAuth2 接入，也支持普通 IMAP 账号。
- 本地存储模式同步入库；远端直连模式每次读取远端，不写本地邮件表。
- 单封外部邮件 50MB 上限，服务端声明的 literal 大小不可信，读取时再限流。
- 外部 IMAP 默认拒绝内网/回环地址，防止 SSRF。

### 安全与可靠性

- TOTP 双因素：开启、关闭都要求当前密码并走再认证限速；已开启 2FA 的账号不会因为全局开关关闭而降级为单密码。
- 权限组/RBAC + 委派管理员 dominance：客服不能重置超管、删超管邮箱或把超管邮箱改到自己名下。
- API Token 独立 scope，邮箱改密使用 `users:reset-password`，不再复用 `mailboxes:write`。
- 设置里的 SMTP/OAuth/Turnstile/外部 IMAP 密钥统一 `enc:v1:` 加密，启动时迁移历史明文。
- SMTP 提交限制并发连接；465 先限流再套 TLS，避免 `*tls.Conn` 被包装后拒绝 AUTH。
- maildir 改名/清理有回滚与水位保护；无 `Message-ID` 的邮件用原文哈希合成稳定 id，改名不会重复导入。
- SQLite 单机部署，已移除 MySQL/PostgreSQL 支持和对应代码。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 前端 | React 18、TypeScript、Vite、shadcn/ui、TanStack Query |
| 后端 | Go 1.27、SQLite、Maildir 同步、SMTP/IMAP 客户端 |
| 邮件栈 | Postfix、Dovecot、Rspamd、Redis 8 |
| 部署 | Docker Compose，单容器 all-in-one（可选多容器 stack） |

## 目录结构

```text
.
├── apps/api              # Go API、SQLite schema、邮件同步与业务逻辑
├── apps/web              # React/Vite 网页邮箱 与管理后台
├── deploy                # Compose、镜像构建、Postfix/Dovecot/Rspamd 配置
├── docs                  # API、部署与原生安装文档
└── .github/workflows     # 镜像构建/发布与 CI
```

## 环境要求

### 开发

- Go 1.27+
- Node.js 20+
- pnpm 10.28.2（可用 corepack 启用）

### 部署

- Docker Engine
- Docker Compose v2
- 可解析的邮件域名
- 可用端口：25 / 465 / 587 / 993 / 995（以及 Web 的 80/443）

> 公网收发还需要正确配置 MX、SPF、DKIM、DMARC，并确认云厂商没有封禁 25 端口。很多云默认封 25，需要单独申请解封。

## 快速开始

### 本地开发

后端：

```bash
cd apps/api
go mod download
go test ./...
go run ./cmd/server
```

前端（新终端）：

```bash
cd apps/web
corepack enable
corepack prepare pnpm@10.28.2 --activate
pnpm install
pnpm run dev
```

访问：

- Web：`http://localhost:5173`
- API：`http://localhost:8080`

默认管理员邮箱是 `admin@lanqin.local`。开发时请显式设置 `LANQIN_ADMIN_PASSWORD`；未设置时后端首次启动会生成随机密码并打印到日志。

### Docker 部署（单容器）

服务器只需要 `deploy/` 下的 Compose 和配置，不需要在服务器上编译：

```bash
cd deploy
cp .env.example .env
# 至少修改：域名、访问地址、管理员邮箱、管理员密码
docker compose pull
docker compose up -d
```

镜像：

```text
ghcr.io/al-xjh/lanqin-email:latest
```

也可以固定到某个发布标签，例如 `ghcr.io/al-xjh/lanqin-email:v1.2.5-lanqin.92`。生产升级前先备份 `.env`，回滚时改回上一版镜像再 `docker compose up -d`。

### 从源码构建

```bash
docker build -f deploy/all-in-one/Dockerfile -t lanqin-email:dense .
docker tag lanqin-email:dense ghcr.io/al-xjh/lanqin-email:latest
```

常用命令：

```bash
# 日志
docker compose logs -f lanqin-email

# 更新
docker compose pull
docker compose up -d

# 健康检查
curl -sk https://mail.example.com/readyz
```

更多部署细节见 [`deploy/README.md`](./deploy/README.md)。

## 首次部署清单

1. 修改 `deploy/.env` 的 `LANQIN_PUBLIC_HOSTNAME`、`LANQIN_PUBLIC_BASE_URL`、`LANQIN_ADMIN_EMAIL`、`LANQIN_ADMIN_PASSWORD`。
2. 生产环境挂载真实证书，设置 `LANQIN_TLS_CERT_FILE` / `LANQIN_TLS_KEY_FILE`。
3. 登录后台添加域名，按提示配置 MX、SPF、DKIM、DMARC，并运行 DNS 检测。
4. 创建邮箱、别名、权限组；按需开启 2FA、Turnstile、邀请码/开放注册。
5. 用后台 SMTP 测试和 网页邮箱 收发各测一封，再测试 465/587、993/995 第三方客户端。
6. 配置备份任务，确认 `.env`、SQLite、Maildir、DKIM 私钥都在备份范围内。

## 关键环境变量

完整配置见 [`deploy/.env.example`](./deploy/.env.example)。最常用的：

| 变量 | 说明 | 默认/示例 |
| --- | --- | --- |
| `LANQIN_IMAGE` | 本分支 all-in-one 镜像 | `ghcr.io/al-xjh/lanqin-email:latest` |
| `LANQIN_PUBLIC_HOSTNAME` | 邮件服务器主机名 | `mail.example.com` |
| `LANQIN_PUBLIC_BASE_URL` | 网页邮箱 对外地址 | `https://mail.example.com` |
| `LANQIN_ADMIN_EMAIL` | 初始管理员邮箱 | `admin@example.com` |
| `LANQIN_ADMIN_PASSWORD` | 初始管理员密码，生产必须修改 | `ChangeMe123!` |
| `LANQIN_DB_PATH` | SQLite 路径 | `/data/lanqin.db` |
| `LANQIN_DATA_DIR` | 数据目录（secret、nonce 等） | `/data` |
| `LANQIN_ALLOW_INSECURE_HTTP` | 允许非 HTTPS Cookie，仅本地调试 | `false` |
| `LANQIN_TRUSTED_PROXY_COUNT` | API 前可信反代层数 | 单容器 Compose 设为 `1` |
| `LANQIN_SMTP_HOST` / `LANQIN_SMTP_PORT` | 网页邮箱 发信 SMTP | `127.0.0.1` / `25` |
| `LANQIN_MAILDIR_ROOT` | Maildir 根目录 | `/var/mail/vhosts` |
| `LANQIN_MAILDIR_SCAN_SECONDS` | Maildir 扫描间隔（秒） | `5` |
| `LANQIN_RSPAMD_DNS` | Rspamd 使用的 DNS | `127.0.0.11:53` |
| `LANQIN_AUTH_POLICY_SECRET` | `/auth-policy` 共享密钥；留空时 entrypoint 自动生成到 `/data/auth-policy-secret` | 空 |
| `LANQIN_EXTERNAL_IMAP_ENABLED` | 启用外部 IMAP | `false` |
| `LANQIN_EXTERNAL_IMAP_MAX_MESSAGE_MB` | 外部 IMAP 单封上限 | `50` |
| `LANQIN_EXTERNAL_IMAP_ALLOW_PRIVATE_HOSTS` / `...ALLOW_INSECURE` | 允许外部 IMAP 连接内网/回环，或允许明文连接；默认都关闭 | `false` |
| `LANQIN_NOTIFICATION_SECRET_KEY` | Telegram Bot Token / TOTP 种子加密根密钥 | 随机长字符串 |

`LANQIN_NOTIFICATION_SECRET_KEY` 由部署者生成并保管，例如：

```bash
openssl rand -base64 32
```

它不会通过后台界面保存。丢失或更换会导致已保存的 Bot Token 无法解密、已加密 TOTP 种子失效；请和 `.env` 一起备份。

## 邮件链路

1. **收件**：Postfix 接收 → Rspamd 评分/标记 → Dovecot 写入 Maildir → API worker 同步到 SQLite → 网页邮箱 展示。
2. **发件**：网页邮箱 调 API → 构造 MIME → 写入 Sent → SMTP 提交给 Postfix 或外部 SMTP → 投递。
3. **第三方客户端**：SMTP 465/587、IMAP 993、POP3 995；证书必须匹配 `LANQIN_PUBLIC_HOSTNAME`。
4. **外部邮箱**：个人设置里添加 Gmail/Outlook 或普通 IMAP；本地模式和远端直连模式二选一。

## 开放 API

外部集成使用 `/api/open/v1` 和带 scope 的 API Token。说明见 [`docs/API.md`](./docs/API.md)，机器可读契约见 [`docs/openapi.json`](./docs/openapi.json)。

发信支持 `Idempotency-Key`；投递状态可通过签名 webhook 或 delivery events 查询。

## 运维

```bash
# 备份（脚本在 deploy/backup/lanqin-backup.sh）
install -m 755 deploy/backup/lanqin-backup.sh /usr/local/bin/lanqin-backup
lanqin-backup

# 查看健康
curl -sk https://mail.example.com/readyz

# 查看日志
docker compose logs --since 10m lanqin-email
```

备份包含 SQLite 一致性快照、`.env`、DKIM 私钥、Maildir 和附件；默认放在 `/root/lanqin-backups/<时间戳>/`，保留 14 天。恢复前先 `docker compose down`，并删除 `data/lanqin.db-wal` / `data/lanqin.db-shm`，否则旧 WAL 会回放到恢复的库上。

## 安全说明

- 生产必须改掉默认管理员密码；`.env`、SQLite、Maildir、DKIM 私钥只给必要用户读权限。
- 推荐把 Web 放在 Nginx/边缘网关后，只暴露 80/443、25、465、587、993、995。
- `/auth-policy` 默认使用 `/data/auth-policy-secret` 共享密钥，secret 为空时直接拒绝；不要把 8080 直接暴露到公网。
- 多容器 split stack 的 Postfix 仍信任 Docker 子网以便 API 投递；如果容器本身被攻陷，应把它当作内网边界处理。

## 与上游的关系

- 上游：`LanQin996/LanQin-Email`，`main` 保持上游原版，用于同步。
- 本分支：`XJH-Email` 与 `ui/dense`，包含界面重排、反垃圾、通知、外部邮箱、安全加固等自定义改动。
- 这些改动不保证会被上游合并；部署本分支请使用本仓库镜像 `ghcr.io/al-xjh/lanqin-email`。

## 许可证

原项目版权归 LanQin996 所有；本分支的修改同样按 [GNU GPL v3 only](./LICENSE) 发布。第三方组件保留各自许可证与版权声明。
