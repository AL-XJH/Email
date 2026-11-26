# Frontend regression checks

These scripts use synthetic API responses intercepted in Playwright. They do not write fixture users, mail, or settings to a running API. No screenshots are taken. Run them against a built preview, with dependencies installed from the repository lockfile.

The application keeps its existing dependency versions. `auth-session-regression.cjs` loads the actual TypeScript modules through the locked TypeScript 6 API alias; the regular build continues to use the TypeScript 7 native compiler.

## VM setup used for verification

Source and locked dependencies: `/srv/fix117-web` on `lanqin-test`.
Node container: `fix117-web-run`, based on `node:26-bookworm-slim`, with `/srv/fix117-web:/work` and host networking.
Preview: `http://127.0.0.1:18082`. The existing `.116` application on port `18081` remains unchanged.

Node 26 has no Corepack; install the package manager with `npm install -g pnpm@12.23.0`, then run `pnpm install --frozen-lockfile` from the isolated workspace root. Do not reuse the older `/srv/lanqin-email/node_modules`.

The browser checks need Playwright and a Chromium headless shell, supplied through `PLAYWRIGHT_MODULE` and `CHROMIUM_PATH`. Keep these test-only dependencies outside the application package and lockfile. Chromium also requires its standard Linux runtime libraries, including NSS, ATK, X11/Xfixes, xkbcommon, GBM, ALSA, Pango and CUPS.

## Commands

Run all checks on the test VM:

```sh
ssh lanqin-test 'docker exec -w /work/apps/web fix117-web-run pnpm run check'
ssh lanqin-test 'docker exec -w /work/apps/web fix117-web-run node scripts/auth-session-regression.cjs'

ssh lanqin-test 'docker exec \
  -e PLAYWRIGHT_MODULE=/work/tests/node_modules/playwright \
  -e CHROMIUM_PATH=/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell \
  -e TMPDIR=/work/tests \
  -e BASE_URL=http://127.0.0.1:18082 \
  -e REPORT_PATH=/work/tests/green-final.json \
  fix117-web-run node /work/apps/web/scripts/regression-117.cjs'

ssh lanqin-test 'docker exec \
  -e PLAYWRIGHT_MODULE=/work/tests/node_modules/playwright \
  -e CHROMIUM_PATH=/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell \
  -e TMPDIR=/work/tests \
  -e BASE_URL=http://127.0.0.1:18082 \
  -e REPORT_PATH=/work/tests/ui-radius-results.json \
  fix117-web-run node /work/apps/web/scripts/ui-radius-regression.cjs'
```

To compare the same browser regression against the old build, change `BASE_URL` to `http://127.0.0.1:18081` and use a separate report path. Failures there are expected.

## .118 再认证回归

`reauth-session-regression.cjs` 使用相同浏览器环境，验证设置页输错当前密码、两步认证校验失败时仍留在当前登录且可以重试；真正会话过期仍跳到登录页。后端 `requireAuth` 用 `X-Lanqin-Session: valid/expired` 区分业务 401 与会话失效，不按中文错误文案或路径猜测。API 未提供该标记的 401 仍按旧协议当作会话失效处理。

```sh
BASE_URL=http://127.0.0.1:18084 \
PLAYWRIGHT_MODULE=/work/tests/node_modules/playwright \
CHROMIUM_PATH=/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell \
node scripts/reauth-session-regression.cjs
```

## .120 异步写信与尺寸回归

`compose-async-regression.cjs` 覆盖「迟到的草稿/回复/全部回复/转发（含附件下载）打开请求不能覆盖后开的新写信窗口」，以及普通发送与定时发送共用同一提交锁：请求等待中按 Esc 或回车不能绕过、编码失败后释放锁并继续自动存草稿、网络失败可直接重试、双击只发一次。定时失败后弹窗保留（toast 会吃掉第一次 Esc），脚本显式点弹窗的「取消」再继续。

`mail-size-regression.cjs` 直接对 `src/lib/mail-size.ts` 断言 25 MiB 附件与 35 MiB 整封预算，含 base64 膨胀和 Unicode 字节长度。

```sh
WEB_DIR=$PWD node scripts/mail-size-regression.cjs
BASE_URL=http://127.0.0.1:18089 PLAYWRIGHT_MODULE=/work/tests/node_modules/playwright \
  CHROMIUM_PATH=/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell \
  node scripts/compose-async-regression.cjs
```

## .122 邮件正文 Referer 回归

`mail-referrer-regression.cjs` 覆盖「远程图片不能带 Referer」：Chrome 对 `srcdoc` 文档忽略 `<iframe referrerPolicy>`，框架里的图片请求仍带 `Referer: https://<host>/`，开了防盗链的发信方（Trakt 的 CDN 实测带 Referer 返回 403、不带返回 200）就会一直破图。修复方式是帧文档自身带 `<meta name="referrer" content="no-referrer">`。脚本起一个 https 探针服务（带 Referer 返回 403，不带返回 1x1 PNG），走 mock API 打开一封含探针图的邮件，断言帧文档声明了该 meta、图片真的加载成功、且解析用的那次请求不带 Referer。探针必须是 https：站点 CSP 为 `img-src 'self' data: blob: https:`，http 探针会先被 CSP 拦掉。`from` 在 mock 里是字符串（`"Sender <a@b>"`）、`to` 是字符串数组、folders/labels 返回数组，否则前端会 `e.trim is not a function` 崩掉。

```sh
BASE_URL=http://127.0.0.1:18089 PLAYWRIGHT_MODULE=/work/tests/node_modules/playwright \
  CHROMIUM_PATH=/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell \
  node scripts/mail-referrer-regression.cjs
```

旧构建上该脚本变红（`frame document must declare <meta name="referrer" ...>`），新构建 1/1 通过。

## Coverage

- Empty and hydrated anonymous sessions, expiration from concurrent protected requests, successful login and preserved return path, failed login without navigation loops, login racing an old `/api/me` response, and failed `/api/me` refetch with cached identity.
- Real `QueryClient`/`QueryObserver` behavior: memory and snapshot removal, concurrent 401 deduplication, rejection of delayed old-account 200 data, immunity of a new session to an old 401, and login 401 isolation.
- Nested mail panel proportions and resize limits.
- Computed per-corner radii for all rendered application HTML elements on the sampled pages, menus and dialogs; light/dark modes; desktop/mobile; profile tabs and admin sections; switches, avatars, badges, select menus, context menus, composer and mobile sheet.
- Original email iframe radius remains 19px in the synthetic message, independently of the application's 6px theme.

Radius counts are sampled element/corner observations, including repeated shared controls across screens; they are not counts of unique component definitions. Email content, SVG/chart geometry and the editor's original content are excluded from application-radius assertions. The existing application locks its UI language to simplified Chinese, so these checks do not claim an English/traditional-Chinese language-switch verification.

These are frontend/module regressions with mocked API responses, not validation of real credentials, SMTP/IMAP, backend persistence or a production deployment.
