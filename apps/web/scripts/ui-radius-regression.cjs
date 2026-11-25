const assert = require("node:assert/strict")
const fs = require("node:fs")
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright")
const { setup, radii, delay } = require("./regression-117.cjs")
const base = process.env.BASE_URL || "http://127.0.0.1:18082"
const output = []
async function sample(page, name) {
  await delay(250)
  const result = await radii(page)
  assert.equal(result.bad.length, 0, JSON.stringify(result.bad.slice(0, 3)))
  output.push({ name, ...result })
}
;(async () => {
  const browser = await chromium.launch({
    executablePath:
      process.env.CHROMIUM_PATH ||
      "/work/browser/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell",
    headless: true,
    args: ["--no-sandbox"],
  })
  const { page, context, state } = await setup(browser, { logged: true })
  page.setDefaultTimeout(5000)
  const assertApp = async () =>
    assert.equal(
      await page.getByText("Unexpected Application Error!").count(),
      0,
      `${page.url()} ${(await page.locator("body").innerText()).slice(0, 1300)}`
    )
  await page.goto(base + "/")
  await page.locator(".mail-list-row").first().waitFor()
  await sample(page, "mail-light")
  await page.getByRole("button", { name: "切换到深色", exact: true }).click()
  await sample(page, "mail-dark")
  await page.getByRole("button", { name: "new-user@example.test", exact: true }).click()
  await page.getByRole("menu").waitFor()
  await sample(page, "mailbox-dropdown")
  await page.keyboard.press("Escape")
  await page.getByRole("combobox").last().click()
  await page.getByRole("listbox").waitFor()
  await sample(page, "pagination-select")
  await page.keyboard.press("Escape")
  await page.locator(".mail-list-row").first().click({ button: "right" })
  await sample(page, "message-context-menu")
  await assertApp()
  await page.keyboard.press("Escape")
  await page.locator(".mail-list-row").first().click()
  await page.frameLocator("iframe").getByText("Original email radius").waitFor()
  const mailRadius = await page
    .frameLocator("iframe")
    .getByText("Original email radius")
    .evaluate((e) => getComputedStyle(e).borderRadius)
  assert.equal(mailRadius, "19px", "mail iframe must preserve its own radius")
  await sample(page, "message-detail-with-mail-iframe")
  await page.goto(base + "/")
  await page.getByRole("button", { name: "写邮件", exact: true }).click({ timeout: 3000 })
  await page.getByRole("dialog").waitFor()
  await sample(page, "compose-dialog")
  await page.getByRole("button", { name: "文字颜色", exact: true }).click()
  await sample(page, "compose-color-menu")
  await page.keyboard.press("Escape")
  await page.keyboard.press("Escape")
  await page.goto(base + "/profile")
  await page.getByRole("switch").first().waitFor()
  await sample(page, "profile-switches-off")
  await page.getByRole("switch").first().click()
  await sample(page, "profile-switches-on")
  await page.setViewportSize({ width: 390, height: 844 })
  await sample(page, "profile-mobile")
  await page.goto(base + "/")
  await page.getByRole("button", { name: "打开导航", exact: true }).waitFor()
  await sample(page, "mail-mobile")
  const widths = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }))
  assert.equal(widths.scroll, widths.client)
  await page.getByRole("button", { name: "打开导航", exact: true }).click()
  await page.getByRole("dialog").waitFor()
  await sample(page, "mail-mobile-sheet")
  await page.keyboard.press("Escape")
  await page.goto(base + "/")
  await page.getByRole("button", { name: "写邮件", exact: true }).click({ timeout: 3000 })
  await page.getByRole("dialog").waitFor()
  await sample(page, "compose-mobile-dialog")
  await page.setViewportSize({ width: 1440, height: 1000 })
  for (const tab of [
    "apiTokens",
    "mailboxes",
    "sharing",
    "clients",
    "signatures",
    "contacts",
    "cleanup",
    "rules",
    "blocked",
    "stats",
  ]) {
    await page.goto(`${base}/profile?tab=${tab}`)
    await delay(400)
    await assertApp()
    await sample(page, `profile-${tab}`)
  }
  for (const section of [
    "users",
    "permissionGroups",
    "domains",
    "mailboxes",
    "aliases",
    "messages",
    "sendAudit",
    "deliveryQueue",
    "settings",
    "backups",
  ]) {
    await page.goto(`${base}/admin?section=${section}`)
    await delay(400)
    await assertApp()
    await sample(page, `admin-${section}`)
  }
  assert.deepEqual(state.errors, [])
  await context.close()
  await browser.close()
  const totals = output.reduce(
    (sum, item) => ({
      elements: sum.elements + item.elements,
      positiveCorners: sum.positiveCorners + item.positiveCorners,
    }),
    { elements: 0, positiveCorners: 0 }
  )
  const report = {
    pass: true,
    samples: output,
    totals,
    mobileWidths: widths,
    mailIframeRadius: mailRadius,
  }
  fs.writeFileSync(
    process.env.REPORT_PATH || "/work/tests/ui-radius-results.json",
    JSON.stringify(report, null, 2)
  )
  console.log(JSON.stringify(report, null, 2))
})().catch((error) => {
  console.error(error)
  process.exit(1)
})
