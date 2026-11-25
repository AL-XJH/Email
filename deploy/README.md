# LanQin Email Docker 部署说明

不使用 Docker、需要完整自建邮件服务时，见[Debian 12 / SQLite 原生部署指南](../docs/native-deployment.md)，包含 API、Nginx、Postfix、Dovecot、Rspamd 和 systemd 配置及验收步骤。原生指南尚未完成真实 Linux 端到端验收。

## 最简单部署：单容器镜像版

服务器上不需要源码构建，只要 `docker-compose.yml` 和 `.env` 即可。

```bash
cd deploy
cp .env.example .env
# 修改 LANQIN_PUBLIC_HOSTNAME / LANQIN_PUBLIC_BASE_URL / LANQIN_ADMIN_EMAIL / LANQIN_ADMIN_PASSWORD
docker compose pull
docker compose up -d
```

也可以使用脚本：

```bash
cd deploy
bash install.sh
```

第一次执行会生成 `.env` 并提示你修改配置；修改完成后再次执行 `bash install.sh`。

默认只启动一个业务容器：

```text
lanqin-email
```

容器内部包含：

- Go API
- Web 静态站点
- Nginx
- Postfix
- Dovecot
- Rspamd

常用命令：

```bash
# 查看日志
docker compose logs -f lanqin-email

# 更新镜像并重启
docker compose pull
docker compose up -d

# 停止
docker compose down
```

## 备份与恢复（宿主 cron 完整恢复集）

这里维护的是 `deploy/backup/` 的 **shell/cron 备份链**，与后台的 age 加密、多地点原生备份是两套独立机制。宿主需要 root、Python 3.9+ 标准库、Docker 和支持 `config --format json` 的 Compose 插件；不需要 PyYAML、宿主 sqlite3 或 redis。只支持当前单容器、SQLite + 内嵌 Redis 布局；发现具名卷、外部 secret/network、遗漏挂载或部署配置与运行态漂移时会报错，不生成“成功但不完整”的恢复集。

### 安装并手动跑一份

仓库更新不会自动更新 `/usr/local/bin`，下列三个文件必须一起安装：

```bash
install -m 700 deploy/backup/lanqin-backup.py /usr/local/bin/lanqin-backup.py
install -m 700 deploy/backup/lanqin-backup.sh /usr/local/bin/lanqin-backup
install -m 700 deploy/backup/lanqin-restore.sh /usr/local/bin/lanqin-restore
APP_DIR=/opt/lanqin-email CONTAINER=lanqin-email-lanqin-email-1 \
  BACKUP_DIR=/root/lanqin-backups KEEP_DAYS=14 /usr/local/bin/lanqin-backup
# 将成功输出的实际目录填入；不输出 .env 或解析后的 Compose 内容。
SET=/root/lanqin-backups/<时间戳>
python3 /usr/local/bin/lanqin-backup.py verify --backup-dir "$SET"
(cd "$SET" && sha256sum -c SHA256SUMS)
```

默认目录为 `/root/lanqin-backups/YYYYmmdd-HHMMSS/`、保留 14 天。滚动清理只删除本格式校验通过的旧集合；旧版集合和其他文件不会被清理。备份根目录必须在应用目录外，目录权限 `0700`、恢复集内全部文件 `0600`。它包含明文密钥与全部邮件，应把**整个集合**复制到受保护的异机位置；同机副本不能防宿主磁盘丢失。SHA256 用于检测损坏，不提供加密或来源认证，只恢复自己保存且可信的集合。

每天 04:10 的配置：

```bash
cat > /etc/cron.d/lanqin-backup <<'CRON'
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
10 4 * * * root /usr/local/bin/lanqin-backup >> /var/log/lanqin-backup.log 2>&1
CRON
chmod 644 /etc/cron.d/lanqin-backup
```

### 备份包含什么、快照边界是什么

- 从运行容器的 Compose 文件标签、`compose config --format json` 和实际 mounts 收集全部 bind source，并核对运行态。包含 `.env`（通知、备份等根密钥）、实际 compose 与 override、nginx 的 80/443 配置、证书和 DKIM 私钥、整个运行数据目录（头像、附件、auth-policy-secret 等）、maildir，以及其他已挂载的运行文件。
- 证书等 symlink 会物化为目标内容，不把对旧主机 `/etc/letsencrypt/...` 的悬空链接留给恢复机。外部 bind source 收入 `_host/`，恢复时重新映射；保留文件/目录类型、UID/GID 和运行文件权限，避免单文件挂载被 Docker 自动创建成目录。
- `backups/{work,artifacts,restore}` 只保留空目录及权限；不会递归打包原生备份历史/临时数据。SQLite 历史 `lanqin.db.*` 回滚副本、WAL/SHM 不打包。Redis 的活动 AOF 不直接归档，而是从停止后的持久化状态生成独立 RDB。原生备份历史如需保留，应使用其自身的异地备份管理。
- **这是短暂停机冷备**：发现和预检完成后停止唯一业务容器；容器停止后才执行 SQLite `.backup` 和 `quick_check`、从 Redis 私有副本生成 RDB，再归档 maildir/附件/配置等全部依赖。容器必须正常退出；容器层 Postfix 队列仍有已接受但未投递邮件时会拒绝发布备份并恢复源容器，需先排空队列再重跑，不能把队列外的邮件漏掉却宣称零丢失。归档完成并校验后恢复原容器。数据库不再先在线快照、邮件稍后在线打包，因此不存在把两者宣称为同一时间点的问题。停机持续时间取决于数据量；SMTP 客户端/上游在此窗口需正常重试。
- 不得同时运行其他写入者、原生备份任务、证书续期或外部脚本修改这些目录。文件与目录元数据会在归档前后核对；发生变化、依赖缺失、任一快照/校验失败或容器意外重启都会返回非零并不发布时间戳目录。正常失败会尝试恢复源容器；宿主断电或 `kill -9` 无法执行清理，需检查容器并手动启动。未完成目录以 `.incomplete-` 开头，不是有效恢复集。
- 恢复集自带恢复工具与严格 SHA256 清单、依赖路径清单、原始解析配置及 `runtime.tar.gz`；不是旧版 `config.tgz/mail.tgz` 格式。旧版缺失依赖的集合不会被自动当作完整备份恢复。

### 丢主机后恢复到新目录

先在目标机安装 Docker、Compose、Python；拉取原集合 `manifest.json` 中的 `image`（只查看镜像名，不要打印 `compose.json`，其中可能有密钥）。恢复按集合记录的稳定 RepoDigest 与平台身份校验镜像；没有 RepoDigest 时验证 config 内容摘要。不同 Docker 存储引擎的本地 image ID 可能不同，不能据此判定内容不同；也不能直接接受已经漂移的 `latest`。镜像不随数据集打包，需保留固定 tag/digest 或另存离线镜像；宿主 DNS 递归解析器、防火墙、证书自动续期账户/定时器和镜像仓库凭据仍需按目标机环境重配，当前证书内容已包含在集合中。

**`--app-dir` 无默认值，必须不存在或为空；脚本不会覆盖旧目录，也不会自动停止现有容器。** 原地恢复前显式停止原应用，保留整个旧目录作回滚点；不能用一个备份覆盖仍被单文件 bind mount 占用的 inode：

```bash
APP_DIR=/opt/lanqin-email
SET=/root/lanqin-backups/<时间戳>
# 确保 APP_DIR/SET 为你要恢复的绝对路径。
# 先校验，再停服务；此命令使用现有部署的 base + 自动加载 override。
python3 "$SET/lanqin-backup.py" verify --backup-dir "$SET"
cd "$APP_DIR"
docker compose down
cd /
# 此目录名需尚不存在；整个目录保留，禁止删库/清 WAL 后在旧目录上覆盖。
mv "$APP_DIR" "${APP_DIR}.before-restore"
python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["image"])' "$SET/manifest.json"
# docker pull <上一行镜像名>；离线环境用 docker load 导入相同镜像。
bash "$SET/lanqin-restore.sh" --backup-dir "$SET" --app-dir "$APP_DIR"
# 恢复程序生成已重映射全部 bind/config/secret 路径的配置；以后使用这份文件。
# 原 compose/override 也保留供审查，但可能含旧主机的绝对路径，勿直接启动它们。
docker compose --project-directory "$APP_DIR" -f "$APP_DIR/docker-compose.restore.json" config -q
docker compose --project-directory "$APP_DIR" -f "$APP_DIR/docker-compose.restore.json" up -d --no-build
# 等待 healthy 后，检查就绪、TLS/IMAP/SMTP 和头像/邮件/附件，完成后再恢复公网流量。
docker compose --project-directory "$APP_DIR" -f "$APP_DIR/docker-compose.restore.json" ps
```

全新主机无需执行 `down/mv`，直接对不存在或空的目标目录运行 `lanqin-restore.sh`。恢复会先完整核对 SHA256、格式与归档路径；拒绝绝对路径、`..`、重复成员、symlink/hardlink 和设备条目。所有文件先放隔离 staging；SQLite `integrity_check`、Redis 恢复和路径类型验证全部通过才移入目标目录。现有目录或活跃挂载不会被覆盖，恢复失败不会留下可被误启用的半成品应用目录。

**旧 v2 集合自带的工具不会随应用升级自动更新。** 恢复升级前的集合时，先在目标机安装当前版本的 `lanqin-backup.py`、`lanqin-backup`、`lanqin-restore`，保留集合全部文件及原 SHA256 清单不变，再运行：

```bash
python3 /usr/local/bin/lanqin-backup.py verify --backup-dir "$SET"
/usr/local/bin/lanqin-restore --backup-dir "$SET" --app-dir "$APP_DIR"
```

这样使用新版的 Redis TTL 和跨 Docker 镜像身份校验，仍严格验证旧集合本身；不要直接覆盖归档内 helper 或重算清单。上述空目录和停服务要求保持不变。旧版 `config.tgz/mail.tgz` 集合不属于 v2，不适用这条兼容入口。

`docker-compose.restore.json` 是运行配置快照，内部含解析后的环境值，权限 `0600`。之后修改运行环境需同时维护该文件或把重映射后的挂载合回原 compose，再检查并重新创建容器；仅编辑 `.env` 不会覆盖已解析进该文件的变量。原始 `.env` 根密钥不得重新生成。单文件 bind 的后续配置更新应原地覆写，或用 `up -d --force-recreate` 重新挂载；不能只 `mv` 新文件后 reload。

### Redis：RDB 恢复后必须先生成 AOF，再启动完整应用

当前配置是 `appendonly yes`。只清旧 AOF、复制 `dump.rdb` 然后直接启动，会使 Redis 优先创建/加载空 AOF 并忽略 RDB，表现为应用 healthy 但 Bayes/灰名单全部清空。恢复脚本实际执行以下流程，不要求操作者临时改生产配置：

1. 从经过校验的集合提取 Redis RDB，确保恢复目录没有旧 AOF；在 `--network none` 的临时容器中执行 `redis-check-rdb`。
2. 临时 Redis 使用原镜像配置，显式 `appendonly no` 启动，加载 RDB；分批扫描所有 DB，记录逐键 `DEBUG DIGEST-VALUE` 和 `PEXPIRETIME` 绝对到期时间（不打印键值，不导出整份值；校验只依赖镜像自带 Python 标准库）。
3. `CONFIG SET appendonly yes` 触发首次 AOF rewrite，等待 `aof_rewrite_in_progress=0`、`aof_rewrite_scheduled=0`、`aof_last_bgrewrite_status=ok`；核对永久键、仍存在键的值摘要与绝对到期时间不变，只允许已经到期的键消失，拒绝额外键或未到期键丢失。
4. 关闭临时 Redis，再按 `appendonly yes` 重启一次，执行同一逐键校验，关闭后保留生成的 AOF。不会延长 TTL 或删除全部过期类键；恢复文件归属还原为原 Redis 目录 UID/GID。
5. 上述验证及 SQLite 检查成功后才发布恢复目录；完整应用正常的 `appendonly yes` 启动会加载已验证的 AOF。

恢复程序从未启动完整邮件服务或开放端口；正式启动仍由操作者在验收窗口完成。过期键自然按 Redis 的绝对过期时间处理，备份不能延长 TTL。

### 可复用的隔离演练

下面脚本创建全新的合成 fixture，所有临时容器均 `--network none`、不发布端口，不读取 demo 或生产数据；保留证据目录，完成后删除自己创建的容器。目录必须尚不存在，镜像应已在测试机上：

```bash
# 把 deploy/backup/ 四个文件复制到测试 VM 的独立目录后：
python3 -u /srv/fix117-backup-code/verify-recovery.py \
  --work-dir /srv/fix117-backup-verification --image lanqin-email:latest-stack
```

成功标准为 `ALL PASS`：完整备份 → `sha256sum -c` → 空目录恢复 → 真正启动恢复后的全部 bind mounts → nginx TLS 配置通过、SQLite 记录和 Redis DB0/DB3 合成键保留；并验证头像/附件/通知密钥/备份密钥/override 存在、证书 symlink 已物化、原生备份目录为空、篡改/逃逸/链接/危险覆盖被拒绝，以及快照失败后源容器恢复且无假成功集合。Redis 另测 200ms/1s 自然到期、二进制键值与多种数据类型，并确认值损坏、永久键或活键缺失、TTL 延长、额外键、AOF 重启后丢状态仍会被拒绝。生产安装后仍须由运维手动执行一次备份并校验，测试 fixture 通过不代替生产恢复演练。

## GHCR 镜像权限

默认镜像：

```text
ghcr.io/lanqin996/lanqin-email:latest
ghcr.io/lanqin996/lanqin-email-api:latest
ghcr.io/lanqin996/lanqin-email-web:latest
ghcr.io/lanqin996/lanqin-email-postfix:latest
ghcr.io/lanqin996/lanqin-email-dovecot:latest
ghcr.io/lanqin996/lanqin-email-rspamd:latest
```

如果拉取时报：

```text
unauthorized
```

说明 GHCR Package 还是私有，二选一：

1. 到 GitHub Packages 把镜像改成 Public。
2. 在服务器登录 GHCR：

```bash
echo "<github_token>" | docker login ghcr.io -u <github_user> --password-stdin
```

## 本地源码构建

如果你是在完整源码仓库里本机构建，使用 build override：

```bash
cd deploy
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

这样会使用 `deploy/all-in-one/Dockerfile` 构建单容器镜像。

## 可选：多容器调试部署

如果需要分别查看 Postfix / Dovecot / Rspamd 日志，可以使用 stack 编排。

拉取镜像版：

```bash
cd deploy
docker compose -f docker-compose.stack.yml up -d
```

源码构建版：

```bash
cd deploy
docker compose -f docker-compose.stack.yml -f docker-compose.stack.build.yml up -d --build
```

## DNS

进入 Web 管理后台后，在域名管理中查看每个域名需要配置的：

- MX
- 域名 SPF TXT
- `LANQIN_PUBLIC_HOSTNAME` 的 HELO SPF TXT
- DKIM TXT
- DMARC TXT

配置完成后点击“检测”。

## 反垃圾（Rspamd）

默认镜像里的 Rspamd 是「只签名、不拦信」的配置，而且有两个会让它**看起来完全没工作**的问题，这个分支把它们修掉了。

### 1. DNS 解析器必须指向本地递归解析器

Spamhaus（`zen.spamhaus.org` / `dbl.spamhaus.org`）和 URIBL 都拒绝来自公共 DNS（1.1.1.1 / 8.8.8.8 / 9.9.9.9）的查询，返回的是 `127.255.255.254`、`127.0.0.1` 这类错误码。Rspamd 识别到这种返回值会把对应黑名单标记为不可靠并停用，日志里只有一句：

```
rspamd_monitored_dns_cb: DNS reply returned 'no error' for zen.spamhaus.org while
'no records with this name' was expected ... (likely DNS spoofing or BL internal issues)
```

结果是**所有 DNSBL 静默失效**，垃圾邮件评分只剩内容规则那几分，永远够不到阈值。转发型的 DNS（smartdns、dnsmasq 转发公共上游）同样不行——Spamhaus 看到的是上游的 IP。

配置项：

```dotenv
# 单容器：docker0 网关上的本地递归解析器
LANQIN_RSPAMD_DNS=172.17.0.1:53
```

宿主机上起一个 unbound（或 knot-resolver / 自建 BIND 递归），只监听 docker 网桥网关或 `127.0.0.1`，并在 `access-control` 里放行 docker 网段即可。改完重启容器，用这条命令验收：

```bash
docker exec <container> getent hosts 2.0.0.127.zen.spamhaus.org
# 正常：返回 127.0.0.2 / 127.0.0.10 之类的真实错误码
# 被拒：返回 127.255.255.254
```

### 2. 判定结果要写进邮件头

Debian 打包的 `milter_headers` 默认 `use = []`，判定完一个头都不写——下游拿不到任何信号。这个分支用 `deploy/rspamd/override.d/milter_headers.conf` 打开：

```
X-Spam-Status: Yes, score=15.5 required=6.0 ...
X-Spam-Level: ***************
X-Spamd-Bar: +++++++++++++++
X-Spamd-Result: default: False [15.50 / 999.00]; RBL_SPAMHAUS_XBL(4.00)...
```

注意 rspamd 3.x 没有 `x-spam` / `x-spam-score` / `x-spam-flag` 这几个 routine，写进去只会得到 `routine does not exist`。

### 3. 收信阶段就分拣进「垃圾邮件」

`deploy/dovecot/sieve/spam.sieve` 通过 `sieve_before` 挂在 LMTP 投递阶段：带 `X-Spam-Status: Yes` 的邮件直接 `fileinto :create "Spam"`。在投递阶段做的好处是不会和 IMAP 客户端搬信打架，也不需要额外的定时任务。

### 4. 阈值

`deploy/rspamd/local.d/actions.conf` 保持保守策略：`reject = 999`（不拒信）、`add_header = 6`（打分即标记）、`greylist = 999`（未启用）。

想更强硬可以把 `reject` 调到 15~20，但那是 SMTP 阶段拒收，会直接产生退信，建议先观察一段时间标记效果再决定。

### 5. Rspamd 从官方仓库装 4.x

Debian bookworm 自带的是 2022 年的 rspamd 3.4，一路陪到发行版 EOL——而反垃圾是规则型软件，版本号本身就等于检测能力。两个 Dockerfile 都改成先加官方仓库再装：

```
deb [signed-by=/etc/apt/keyrings/rspamd.gpg] https://rspamd.com/apt-stable/ bookworm main
```

构建时可用 `--build-arg RSPAMD_REPO=...` 换仓库地址（比如内网镜像）。

验版本：

```bash
docker exec <container> rspamd --version
# Rspamd daemon version 4.2.3
```

### 6. Redis + Bayes

`milter_headers` 之类的模块只要配置就能跑，但 Bayes、灰名单、ratelimit、reputation 都需要一个跨进程的后端存储——rspamd 是多 worker 模型，进程间不共享内存，重启即归零。

**单容器模式**：镜像里已经装了 redis-server（官方源，8.x），由 supervisord 托管，配置在 `/etc/redis-lanqin.conf`——只 bind `127.0.0.1`、不映射端口、AOF 落在 `/data/redis`。rspamd 通过 `LANQIN_REDIS_ADDR=127.0.0.1:6379` 连它。**整个服务仍然只有一个容器。**

**多容器（stack）模式**：`docker-compose.stack.yml` 里单独有一个 `redis` 服务（`./redis` 挂载 + `appendonly yes`），此时把 `LANQIN_REDIS_ADDR` 改成 `redis:6379`。

`LANQIN_REDIS_ADDR` 留空则自动摘掉相关配置，rspamd 退回纯规则匹配。

Bayes 的自动学习用的是 rspamd 4.x 原生 `autolearn`，不需要外部脚本跑 `rspamc learn_*`：

| 参数 | 值 | 含义 |
|---|---|---|
| `spam_threshold` | 12.0 | 分数 ≥ 12 当 spam 学 |
| `junk_threshold` | 6.0 | 已进垃圾箱且 ≥ 6 当 spam 学 |
| `ham_threshold` | 1.0 | 分数 ≤ 1 当 ham 学 |
| `check_balance` | true | 保持 spam/ham 学习量平衡 |

门槛刻意定得比 `add_header`(6) 高：6~12 是"不确定"区间，拿这区间去训练等于把自己的犹豫当标准答案。

`min_learns = 200`，所以在攒够样本之前 Bayes 一个分都不出——**这是设计如此，不是没配上**。

验证：

```bash
docker exec <container> rspamc stat | grep -E "learned|learns"
docker exec <container> redis-cli -h redis info keyspace
```

灰名单（greylist）现在也具备条件了，但它会延迟首次通信的陌生发件人几分钟，默认仍然关着（`actions.conf` 里 `greylist = 999`）。要开就把这个值调到 4~5。

## Linux.do SSO

1. 确认 `LANQIN_PUBLIC_BASE_URL` 是用户实际访问的 HTTPS 地址。
2. 前往 [Connect.Linux.Do](https://connect.linux.do/) 的“我的应用接入 > 申请新接入”创建应用。
3. 使用管理后台“系统设置 > 安全”中显示的只读地址作为回调地址，格式为：

```text
https://你的站点/api/auth/linuxdo/callback
```

4. 在同一区域填写 Client ID、Client Secret，开启“Linux.do 登录”；按需独立开启“允许 Linux.do 用户注册本站账号”。

Client Secret 保存在现有 `system_settings` 中，接口和前端只显示“已配置”状态，后续留空保存不会清除。关闭 SSO 不会删除 `oauth_identities` 中已有绑定。既有用户应先使用本站邮箱密码登录，再在个人设置中绑定；系统不会按 Linux.do 用户名自动匹配本站账号。启用了本地 TOTP 的用户通过 SSO 登录时仍需输入 TOTP。

## 邀请码注册

在管理后台“系统设置 > 安全”开启“邀请码注册”，然后在同一区域创建邀请码并设置可用次数。邀请码会完整保存在数据库中，管理员之后仍可查看和复制；每次普通账号注册成功会原子扣减一次。普通“开放注册”开启时不要求邀请码，两种开关都关闭时普通注册入口不可用。Linux.do 注册不受邀请码开关影响。

## 邮件服务边界

- Postfix 读取 `/data/lanqin.db` 中的 `domains`、`mailboxes`、`aliases`。
- Dovecot 读取同一个 SQLite 数据库进行邮箱认证，并使用 `/var/mail/vhosts` 作为 Maildir 根目录。
- 第三方客户端可使用 IMAP SSL `993`、POP3 SSL `995`、SMTP SSL `465` 或 Submission `587`。
- Rspamd 通过 milter 接入 Postfix，负责 DKIM 签名和垃圾邮件标记。
- Rspamd 会周期性从 SQLite 导出域名 DKIM 私钥到容器内 `/var/lib/rspamd/dkim`。
- 后台“别名转发”是 Postfix 地址别名投递；用户收件箱保留邮件后再自动转发，请先在“个人设置 → 收件规则”中通过验证码验证目标邮箱，再添加“自动转发到”动作。历史邮件、自动生成邮件和已自动转发邮件不会再次转发。
- Go API 是 Webmail 和管理后台入口；浏览器不直接连接 SMTP/IMAP/POP3。
- Go API 会读取 `LANQIN_MAILDIR_ROOT=/var/mail/vhosts`，周期扫描 Maildir，把 Postfix/Dovecot 入站邮件同步成 Webmail 索引。
- 第三方客户端可通过 LanQin API 提供的 SMTP `465/587` 发信；Webmail/API 和第三方客户端的“已发送”都由 API 写入，外发投递进入发送队列并由 API worker relay/retry，客户端后续 IMAP APPEND 到 Sent 会按 `Message-ID` 去重。
- 用户可在个人邮箱管理中接入外部 IMAP 账号；默认关闭，可在后台“系统设置 > 外部 IMAP”开启并配置密钥/OAuth。本地存储模式会同步到 LanQin，远端直连模式每次从远端读取。启用前必须配置外部 IMAP 密码加密密钥，默认不允许连接 localhost / 内网 / link-local IMAP 主机。Gmail / Microsoft 365 / Outlook OAuth2 需要在对应控制台配置回调地址：`/api/external-imap-oauth/gmail/callback` 或 `/api/external-imap-oauth/outlook/callback`。
- send-as v1 支持本人邮箱、启用的别名转发 source 指向本人邮箱，或数据库表 `send_as_grants` 中显式授权的地址。

## 邮件客户端 TLS 证书

Web 站点可以由宿主机 Nginx / 宝塔反代到容器 `80`，但 SMTP/IMAP/POP3 端口不会使用 Web 反代的证书。
如果第三方客户端连接 `993/995` 时提示证书是 `localhost`，说明 Dovecot 仍在使用容器自带的测试证书。LanQin API 的 SMTP `465/587` submission 不会使用自签测试证书；启用前必须配置可读的真实证书。

生产环境请把域名证书挂载进容器，并在 `.env` 指向证书文件：

```env
LANQIN_TLS_CERT_FILE=/certs/fullchain.pem
LANQIN_TLS_KEY_FILE=/certs/privkey.pem
LANQIN_SUBMISSION_ADDR=:587
LANQIN_SUBMISSION_TLS_ADDR=:465
```

单容器示例：

```yaml
services:
  lanqin-email:
    volumes:
      - ./data:/data
      - ./mail:/var/mail/vhosts
      - ./dkim:/var/lib/rspamd/dkim
      - /etc/letsencrypt:/etc/letsencrypt:ro
```

证书域名必须覆盖 `LANQIN_PUBLIC_HOSTNAME`。更新后执行：

```bash
docker compose up -d --force-recreate
```

## SMTP 发信排查

单容器部署时，Webmail 发信默认提交给同容器内的 Postfix：

```env
LANQIN_SMTP_HOST=127.0.0.1
LANQIN_SMTP_PORT=25
LANQIN_SMTP_REQUIRE_TLS=false
```

如需把上游服务商或 DSN 处理器的最终送达、退信、投诉、拒收事件写回开放 API，请设置：

```env
LANQIN_DELIVERY_WEBHOOK_SECRET=replace-with-a-long-random-secret
```

回调地址、签名算法和事件格式见仓库中的 `docs/API.md` 与 `docs/openapi.json`。该接口未配置密钥时返回 `503`。

如需把状态变化主动推送到集成方，可额外设置：

```env
LANQIN_STATUS_WEBHOOK_URL=https://integration.example.com/hooks/lanqin
LANQIN_STATUS_WEBHOOK_SECRET=replace-with-another-long-random-secret
LANQIN_STATUS_WEBHOOK_ALLOW_PRIVATE_HOSTS=false
```

事件先写入数据库 outbox，再由后台 worker 投递；非 2xx 响应会按退避策略重试，最多 10 次。默认只允许公网 HTTPS，禁止重定向、URL 用户信息和私网/本机目标。只有可信内网或本地测试才应开启 `LANQIN_STATUS_WEBHOOK_ALLOW_PRIVATE_HOSTS`。

如需启用用户级 Telegram 收件通知，先配置独立的加密主密钥：

```env
LANQIN_NOTIFICATION_SECRET_KEY=replace-with-a-long-random-secret
```

该值不需要向 Telegram 申请：它是部署级 AES-GCM 根密钥，只用于加密数据库中的 Bot Token。请在部署机使用密码学安全随机源生成并写入 `.env`（PowerShell：`$b = New-Object byte[] 32; [Security.Cryptography.RandomNumberGenerator]::Fill($b); [Convert]::ToBase64String($b)`；OpenSSL：`openssl rand -base64 32`），然后重启 API。务必备份并限制 `.env` 权限；更换或丢失此密钥会使已有 Token 无法解密。出于密钥分层和轮换安全，系统不会通过后台 UI 保存该根密钥。

重启服务后，拥有“管理收件规则”权限的用户可在“个人中心 > 收件规则”中填写自己的 Bot Token 和 Chat ID，并把“发送 Telegram 通知”添加为规则动作。Token 使用该主密钥加密后保存；更换或丢失主密钥会导致已有 Token 无法解密。通知先写入 outbox，再异步投递并按退避策略最多重试 10 次。

Split stack 使用 `docker-compose.stack.yml` 时，API 容器默认会把 `LANQIN_SMTP_HOST` 覆盖为 `postfix`，让 Webmail 和 SMTP 提交都 relay 到 Postfix service。只有改用外部 SMTP 时才需要在 `.env` 明确填写 `LANQIN_STACK_SMTP_HOST` / `LANQIN_STACK_SMTP_PORT`。

如果发送队列里出现 relay 失败，通常是 Postfix 会话被中断或外部 SMTP 配置错误。优先检查：

```bash
docker compose exec lanqin-email supervisorctl status
docker compose exec lanqin-email postconf -M smtp/inet
# SMTP 提交 465/587 由 LanQin API 提供，不再由 Postfix 监听。
docker compose exec lanqin-email sqlite3 /data/lanqin.db "select key,value from system_settings where key like 'smtp%' order by key;"
docker compose exec lanqin-email sqlite3 /data/lanqin.db "select status,attempt_count,last_error from send_queue order by created_at desc limit 10;"
docker compose logs --tail=200 lanqin-email
```

确认后台“系统设置”里没有把本机 Postfix 的 `SMTP Require TLS` 打开；本机 `127.0.0.1:25` 必须保持 TLS=false。

## 数据库版本选择

默认 SQLite 部署：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

MySQL 8.4 部署需要先在 `deploy/.env` 设置 `LANQIN_MYSQL_ROOT_PASSWORD` 和 `LANQIN_MYSQL_PASSWORD`：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.mysql.yml up -d
```

PostgreSQL 16 部署需要先在 `deploy/.env` 设置 `LANQIN_POSTGRES_ADMIN_PASSWORD` 和 `LANQIN_POSTGRES_PASSWORD`。应用账号不是 PostgreSQL 超级用户；应用密码必须使用字母、数字、点、下划线、波浪线或连字符：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.postgres.yml up -d
```

两种模式都保留单个 `lanqin-email` 主容器，只额外增加一个数据库容器。数据库使用独立 Docker volume，端口默认不暴露到宿主机。首次启动必须使用空数据库，应用不会自动迁移 SQLite 数据；不要同时叠加 MySQL 和 PostgreSQL 两个 override。已有数据库 volume 的账号密码不会因修改 `.env` 自动轮换，应先在数据库内更新密码。

## 生产注意

- 建议在服务器或边缘网关配置 HTTPS。
- 云厂商通常默认封禁 25 端口，需要单独申请解封。
- SQLite 适合轻量单机部署；MySQL/PostgreSQL 版本必须使用对应 Compose override，使 API、Postfix、Dovecot 与 Rspamd 保持同库。

## 健康检查

API 提供三个无需认证的健康端点，响应不会包含数据库 DSN、文件路径或底层错误：

- `GET /livez`：只表示 API 进程仍在运行，始终不检查外部依赖，适合作为 Kubernetes liveness probe。
- `GET /readyz`：检查数据库连接、schema 初始化/外部数据库版本、附件与 Maildir 目录，以及所有实际启用的后台 worker；任一检查失败返回 `503`，适合作为 readiness probe 和负载均衡摘流量依据。
- `GET /healthz`：保留给旧部署的兼容入口，语义与 `/readyz` 相同。

Compose 已为单容器及 split stack 配置 healthcheck。单容器还会检查 Nginx、Postfix、Dovecot 和 Rspamd；split stack 会分别检查 API、Web、反向代理及邮件组件。初次启动或数据库迁移期间短暂显示 `starting` 属正常现象。

