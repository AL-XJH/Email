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

## Coverage

- Empty and hydrated anonymous sessions, expiration from concurrent protected requests, successful login and preserved return path, failed login without navigation loops, login racing an old `/api/me` response, and failed `/api/me` refetch with cached identity.
- Real `QueryClient`/`QueryObserver` behavior: memory and snapshot removal, concurrent 401 deduplication, rejection of delayed old-account 200 data, immunity of a new session to an old 401, and login 401 isolation.
- Nested mail panel proportions and resize limits.
- Computed per-corner radii for all rendered application HTML elements on the sampled pages, menus and dialogs; light/dark modes; desktop/mobile; profile tabs and admin sections; switches, avatars, badges, select menus, context menus, composer and mobile sheet.
- Original email iframe radius remains 19px in the synthetic message, independently of the application's 6px theme.

Radius counts are sampled element/corner observations, including repeated shared controls across screens; they are not counts of unique component definitions. Email content, SVG/chart geometry and the editor's original content are excluded from application-radius assertions. The existing application locks its UI language to simplified Chinese, so these checks do not claim an English/traditional-Chinese language-switch verification.

These are frontend/module regressions with mocked API responses, not validation of real credentials, SMTP/IMAP, backend persistence or a production deployment.
