// Actual built frontend + explicit synthetic APIs; no live account is modified.
const assert = require("node:assert/strict")
const fs = require("node:fs")
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright")
const { setup, delay } = require("./regression-117.cjs")
const base = process.env.BASE_URL || "http://127.0.0.1:18084"
const results = []
async function scenario(name, run) {
  try {
    await run()
    results.push({ name, pass: true })
  } catch (error) {
    results.push({ name, pass: false, error: error.message })
  }
}
;(async () => {
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_PATH,
    headless: true,
    args: ["--no-sandbox"],
  })
  await scenario("wrong-current-password-stays-and-can-retry", async () => {
    const { context, page } = await setup(browser, { logged: true })
    try {
      let attempts = 0
      await context.route("**/api/me/password", async (route) => {
        attempts++
        await route.fulfill({
          status: attempts === 1 ? 401 : 200,
          contentType: "application/json",
          headers: { "X-Lanqin-Session": "valid" },
          body: JSON.stringify(attempts === 1 ? { error: "当前密码错误" } : { ok: true }),
        })
      })
      await page.goto(`${base}/profile?tab=profile`)
      const form = page.locator("form").filter({ has: page.locator('input[name="newPassword"]') })
      await form.locator('input[name="currentPassword"]').fill("mistyped")
      await form.locator('input[name="newPassword"]').fill("Replacement123!")
      await form.locator('input[name="confirmPassword"]').fill("Replacement123!")
      await form.getByRole("button", { name: "更新密码", exact: true }).click()
      await delay(700)
      assert.equal(new URL(page.url()).pathname, "/profile")
      assert.match(await page.locator("body").innerText(), /当前密码错误/)
      assert.equal(await form.count(), 1)
      await page.locator("[toast-close]").last().click()
      await form.locator('input[name="currentPassword"]').fill("correct-fixture")
      await form.getByRole("button", { name: "更新密码", exact: true }).click()
      await delay(400)
      assert.equal(attempts, 2)
      assert.equal(new URL(page.url()).pathname, "/profile")
      assert.match(await page.locator("body").innerText(), /密码已更新/)
    } finally {
      await context.close()
    }
  })
  await scenario("two-factor-current-password-error-keeps-session", async () => {
    const { context, page } = await setup(browser, { logged: true })
    try {
      await context.route("**/api/me/2fa/setup", async (route) =>
        route.fulfill({
          status: 401,
          contentType: "application/json",
          headers: { "X-Lanqin-Session": "valid" },
          body: JSON.stringify({ error: "当前密码错误" }),
        })
      )
      await page.goto(`${base}/profile?tab=profile`)
      await page.getByPlaceholder("当前登录密码", { exact: true }).fill("mistyped")
      await page.getByRole("button", { name: "启用双因素认证", exact: true }).click()
      await delay(700)
      assert.equal(new URL(page.url()).pathname, "/profile")
      assert.match(await page.locator("body").innerText(), /当前密码错误/)
    } finally {
      await context.close()
    }
  })
  await scenario("expired-session-on-password-request-still-redirects", async () => {
    const { context, page, state } = await setup(browser, { logged: true })
    try {
      await context.route("**/api/me/password", async (route) => {
        state.logged = false
        await route.fulfill({
          status: 401,
          contentType: "application/json",
          headers: { "X-Lanqin-Session": "expired" },
          body: JSON.stringify({ error: "authentication required" }),
        })
      })
      await page.goto(`${base}/profile?tab=profile`)
      const form = page.locator("form").filter({ has: page.locator('input[name="newPassword"]') })
      await form.locator('input[name="currentPassword"]').fill("fixture")
      await form.locator('input[name="newPassword"]').fill("Replacement123!")
      await form.locator('input[name="confirmPassword"]').fill("Replacement123!")
      await form.getByRole("button", { name: "更新密码", exact: true }).click()
      await page.waitForURL("**/login", { timeout: 3000 })
      await page.locator('input[name="email"]').waitFor()
    } finally {
      await context.close()
    }
  })
  await browser.close()
  const report = { passed: results.filter((x) => x.pass).length, failed: results.filter((x) => !x.pass).length, results }
  if (process.env.REPORT_PATH) fs.writeFileSync(process.env.REPORT_PATH, JSON.stringify(report, null, 2))
  console.log(JSON.stringify(report, null, 2))
  if (report.failed) process.exitCode = 1
})().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
