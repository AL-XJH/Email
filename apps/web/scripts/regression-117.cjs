// Synthetic browser regressions; API calls are intercepted and never reach demo data.
const assert = require("node:assert/strict")
const fs = require("node:fs")
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright")
const base = process.env.BASE_URL || "http://127.0.0.1:18082"
const results = []
const user = (id = "new-user") => ({
  id,
  email: `${id}@example.test`,
  displayName: id,
  role: "admin",
  permissions: [],
  limits: { maxMailboxes: 0 },
  mailboxCount: 1,
  mailboxQuotaBonus: 0,
})
const mailbox = (id = "new-user") => ({
  id: `${id}-mailbox`,
  address: `${id}@example.test`,
  displayName: id,
  status: "active",
  userId: id,
})
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
async function scenario(name, run) {
  try {
    const detail = await run()
    results.push({ name, pass: true, detail })
  } catch (error) {
    results.push({ name, pass: false, error: error.message })
  }
}
async function setup(browser, { logged = false, hydrated = false } = {}) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const state = { logged, id: "new-user", badLogin: false, requests: [], delayMe: 0, meStatus: 0 }
  if (hydrated) {
    await context.addInitScript(() => {
      if (sessionStorage.getItem("fixture-seeded")) return
      sessionStorage.setItem("fixture-seeded", "1")
      const query = (queryKey, data) => ({
        queryKey,
        queryHash: JSON.stringify(queryKey),
        state: {
          data,
          dataUpdateCount: 1,
          dataUpdatedAt: Date.now(),
          error: null,
          errorUpdateCount: 0,
          errorUpdatedAt: 0,
          fetchFailureCount: 0,
          fetchFailureReason: null,
          fetchMeta: null,
          isInvalidated: false,
          status: "success",
          fetchStatus: "idle",
        },
      })
      localStorage.setItem(
        "lanqin:query-cache",
        JSON.stringify({
          at: Date.now(),
          state: {
            mutations: [],
            queries: [
              query(["me"], {
                user: { id: "old-user", email: "old-user@example.test", role: "admin" },
              }),
              query(["mailboxes"], {
                items: [{ id: "old-mailbox", address: "old-user@example.test" }],
              }),
            ],
          },
        })
      )
    })
  }
  await context.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname
    state.requests.push(path)
    let body = { items: [] },
      status = 200
    if (path === "/api/me") {
      const loggedAtStart = state.logged
      if (state.delayMe) await delay(state.delayMe)
      status = state.meStatus || (loggedAtStart ? 200 : 401)
      body = loggedAtStart
        ? { user: user(state.id), preferences: { showRemoteImages: false } }
        : { error: "unauthorized" }
    } else if (path === "/api/auth/login") {
      status = state.badLogin ? 401 : 200
      if (state.badLogin) body = { error: "邮箱或密码错误" }
      else {
        state.logged = true
        body = { user: user(state.id) }
      }
    } else if (path === "/api/auth/logout") {
      state.logged = false
      body = { ok: true }
    } else if (path === "/api/public/settings") {
      body = { siteTitle: "Regression fixture", openRegistration: true, turnstileEnabled: false }
    } else if (path.includes("mailboxes")) {
      status = state.logged ? 200 : 401
      body = state.logged ? { items: [mailbox(state.id)] } : { error: "unauthorized" }
    } else if (path === "/api/mail/messages/fixture-message") {
      body = {
        id: "fixture-message",
        mailboxId: `${state.id}-mailbox`,
        folderId: "inbox",
        folder: "Inbox",
        messageUid: "fixture-uid",
        messageId: "fixture@example.test",
        imapUid: 1,
        imapModseq: 1,
        from: "Sender <sender@example.test>",
        to: [`${state.id}@example.test`],
        cc: [],
        subject: "Regression message",
        snippet: "Synthetic text",
        bodyText: "Synthetic text",
        bodyHtml:
          '<div style="border-radius:19px;border:1px solid red">Original email radius</div>',
        receivedAt: "2026-11-20T08:00:00Z",
        sentAt: "2026-11-20T08:00:00Z",
        sizeBytes: 100,
        isRead: true,
        isStarred: false,
        hasAttachments: false,
        attachments: [],
        labels: [],
      }
    } else if (path.endsWith("/messages")) {
      status = state.logged ? 200 : 401
      body = state.logged
        ? {
            items: [
              {
                id: "fixture-message",
                mailboxId: `${state.id}-mailbox`,
                folder: "Inbox",
                from: "Sender <sender@example.test>",
                to: [`${state.id}@example.test`],
                subject: "Regression message",
                snippet: "Synthetic text",
                receivedAt: "2026-11-20T08:00:00Z",
                isRead: true,
                isStarred: false,
                hasAttachments: false,
                labels: [],
              },
            ],
            totalCount: 1,
          }
        : { error: "unauthorized" }
    } else if (path.includes("/stats")) {
      body = {
        total: 1,
        unread: 0,
        folders: [],
        totalMessages: 1,
        unreadMessages: 0,
        starredMessages: 0,
        storageBytes: 100,
        quotaBytes: 1000,
        attachmentMessages: 0,
        byFolder: [{ folder: "Inbox", role: "inbox", count: 1, bytes: 100 }],
      }
    }
    await route
      .fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
      .catch(() => {})
  })
  const page = await context.newPage()
  page.setDefaultTimeout(4000)
  state.errors = []
  page.on("pageerror", (error) => state.errors.push(error.message))
  return { context, page, state }
}
async function login(page) {
  await page.locator("input[name=email]").fill("new-user@example.test")
  await page.locator("input[name=password]").fill("synthetic-password")
  await page.getByRole("button", { name: "登录", exact: true }).click()
}
async function radii(page) {
  return page.evaluate(() => {
    const buckets = {},
      bad = []
    let elements = 0,
      positiveCorners = 0
    for (const element of document.querySelectorAll("body *")) {
      if (
        element.closest(".mail-html, .ProseMirror") ||
        element.namespaceURI !== "http://www.w3.org/1999/xhtml"
      )
        continue
      const rect = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      if (!rect.width || !rect.height || style.visibility === "hidden" || style.display === "none")
        continue
      elements++
      for (const corner of [
        "borderTopLeftRadius",
        "borderTopRightRadius",
        "borderBottomLeftRadius",
        "borderBottomRightRadius",
      ]) {
        const value = style[corner]
        buckets[value] = (buckets[value] || 0) + 1
        if (parseFloat(value) > 0) {
          positiveCorners++
          if (value !== "6px" && bad.length < 30)
            bad.push({ tag: element.tagName, class: element.className, corner, value })
        }
      }
    }
    return { elements, positiveCorners, buckets, bad }
  })
}
module.exports = { setup, radii, delay }
if (require.main === module)
  (async () => {
    const browser = await chromium.launch({
      executablePath:
        process.env.CHROMIUM_PATH ||
        "/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell",
      headless: true,
      args: ["--no-sandbox"],
    })
    for (const hydrated of [false, true]) {
      await scenario(`anonymous-${hydrated ? "hydrated" : "empty"}-redirect`, async () => {
        const { context, page, state } = await setup(browser, { hydrated })
        try {
          await page.goto(`${base}/profile?tab=security#fixture`)
          await page.waitForURL("**/login", { timeout: 3000 })
          await page.locator("input[name=email]").waitFor()
          assert.equal(await page.locator("input[name=email]").count(), 1)
          const snapshot = await page.evaluate(() => localStorage.getItem("lanqin:query-cache"))
          assert.ok(!snapshot?.includes("old-user"), "old account persisted after 401")
          assert.deepEqual(state.errors, [])
          return {
            url: page.url(),
            meRequests: state.requests.filter((p) => p === "/api/me").length,
          }
        } finally {
          await context.close()
        }
      })
    }
    await scenario("login-success-with-return-path", async () => {
      const { context, page, state } = await setup(browser)
      try {
        await page.goto(`${base}/profile?tab=profile#return-fixture`)
        await page.waitForURL("**/login", { timeout: 3500 })
        await page.locator("input[name=email]").waitFor()
        await login(page)
        await page.waitForURL(`${base}/profile?tab=profile#return-fixture`, { timeout: 3500 })
        await page.locator("[data-panel]").first().waitFor()
        assert.deepEqual(state.errors, [])
        return { url: page.url(), meRequests: state.requests.filter((p) => p === "/api/me").length }
      } finally {
        await context.close()
      }
    })
    await scenario("bad-login-does-not-loop", async () => {
      const { context, page, state } = await setup(browser)
      try {
        state.badLogin = true
        await page.goto(`${base}/login`)
        await login(page)
        await delay(700)
        assert.equal(page.url(), `${base}/login`)
        assert.ok((await page.locator("body").innerText()).includes("登录失败"))
        assert.ok(state.requests.filter((p) => p === "/api/me").length <= 2, "401 request loop")
        assert.deepEqual(state.errors, [])
        return { requests: state.requests }
      } finally {
        await context.close()
      }
    })
    await scenario("expired-session-concurrent-requests-and-account-switch", async () => {
      const { context, page, state } = await setup(browser, { logged: true })
      try {
        await page.goto(`${base}/`)
        await page.locator(".mail-list-row").first().waitFor()
        state.logged = false
        await page.locator('[title="刷新邮件"]').evaluate((element) => element.click())
        await page.waitForURL("**/login", { timeout: 3500 })
        await page.locator("input[name=email]").waitFor()
        const duringExpiry = await page.evaluate(() => localStorage.getItem("lanqin:query-cache"))
        assert.ok(!duringExpiry?.includes("new-user@example.test"), "expired account persisted")
        state.id = "second-user"
        await login(page)
        await page.waitForURL(`${base}/`, { timeout: 3500 })
        await page.locator(".mail-list-row").first().waitFor()
        await delay(1000)
        assert.equal(page.url(), `${base}/`)
        const snapshot = await page.evaluate(() => localStorage.getItem("lanqin:query-cache"))
        assert.ok(!snapshot?.includes("new-user@example.test"), "previous account re-persisted")
        assert.ok(snapshot?.includes("second-user@example.test"))
        assert.deepEqual(state.errors, [])
        return { url: page.url(), meRequests: state.requests.filter((p) => p === "/api/me").length }
      } finally {
        await context.close()
      }
    })
    await scenario("login-while-initial-me-401-is-pending", async () => {
      const { context, page, state } = await setup(browser)
      try {
        state.delayMe = 500
        await page.goto(`${base}/login`)
        await login(page)
        state.delayMe = 0
        await page.waitForURL(`${base}/`, { timeout: 3500 })
        await page.locator("[data-panel]").first().waitFor()
        await delay(800)
        assert.equal(page.url(), `${base}/`)
        assert.deepEqual(state.errors, [])
        return { url: page.url() }
      } finally {
        await context.close()
      }
    })
    await scenario("failed-me-refetch-does-not-expose-hydrated-user", async () => {
      const { context, page, state } = await setup(browser, { logged: true, hydrated: true })
      try {
        state.meStatus = 500
        await page.goto(`${base}/login`)
        await page.locator("input[name=email]").waitFor()
        await delay(1800)
        assert.equal(page.url(), `${base}/login`)
        assert.ok(!(await page.locator("body").innerText()).includes("old-user@example.test"))
        assert.deepEqual(state.errors, [])
        return { url: page.url() }
      } finally {
        await context.close()
      }
    })
    await scenario("nested-panels-percentages-and-drag", async () => {
      const { context, page, state } = await setup(browser, { logged: true })
      try {
        await page.goto(`${base}/`)
        await page.locator("[data-panel]").first().waitFor()
        const sizes = () =>
          page
            .locator("[data-panel]")
            .evaluateAll((es) =>
              es.map((e) => ({
                w: e.getBoundingClientRect().width,
                parent: e.parentElement.getBoundingClientRect().width,
              }))
            )
        const before = await sizes()
        assert.ok(
          before[2].w / before[2].parent >= 0.219 && before[2].w / before[2].parent <= 0.381,
          JSON.stringify(before)
        )
        const box = await page.getByRole("separator").last().boundingBox()
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
        await page.mouse.down()
        await page.mouse.move(box.x + 400, box.y + box.height / 2, { steps: 12 })
        await page.mouse.up()
        const after = await sizes()
        assert.ok(after[2].w - before[2].w > 50, JSON.stringify({ before, after }))
        assert.ok(after[2].w / after[2].parent <= 0.381)
        assert.deepEqual(state.errors, [])
        return { before, after }
      } finally {
        await context.close()
      }
    })
    for (const path of ["/login", "/register", "/", "/profile", "/admin", "/missing"]) {
      await scenario(`radii-${path}`, async () => {
        const { context, page, state } = await setup(browser, {
          logged: !["/login", "/register", "/missing"].includes(path),
        })
        try {
          await page.goto(base + path)
          await delay(650)
          const result = await radii(page)
          assert.equal(result.bad.length, 0, JSON.stringify(result.bad.slice(0, 3)))
          assert.deepEqual(state.errors, [])
          return result
        } finally {
          await context.close()
        }
      })
    }
    await browser.close()
    const output = {
      base,
      results,
      passed: results.filter((r) => r.pass).length,
      failed: results.filter((r) => !r.pass).length,
    }
    fs.writeFileSync(
      process.env.REPORT_PATH || "/work/tests/regression-results.json",
      JSON.stringify(output, null, 2)
    )
    console.log(JSON.stringify(output, null, 2))
    process.exitCode = output.failed ? 1 : 0
  })().catch((error) => {
    console.error(error)
    process.exitCode = 1
  })
