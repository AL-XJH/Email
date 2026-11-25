// Actual built web app; all API calls are synthetic and intercepted before network.
const assert = require("node:assert/strict")
const fs = require("node:fs")
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright")
const { setup, delay } = require("./regression-117.cjs")
const base = process.env.BASE_URL || "http://192.168.100.10:18081"
const report = []
const full = {
  id: "fixture-message", mailboxId: "new-user-mailbox", mailboxAddress: "new-user@example.test",
  folder: "Inbox", from: "sender@example.test", to: ["new-user@example.test"], cc: [], bcc: [],
  subject: "Invoice fixture", snippet: "Invoice body", bodyText: "Invoice body",
  bodyHtml: '<div><style>.invoice-cell{color:#123456}</style><table width="600"><tbody><tr><td class="invoice-cell" style="background:#ffeedd;width:300px">Invoice left</td><td style="width:300px">Invoice right</td></tr></tbody></table></div>',
  receivedAt: "2026-11-20T08:00:00Z", isRead: true, hasAttachments: true,
  attachments: [{ id: "invoice-attachment", filename: "invoice.txt", contentType: "text/plain", sizeBytes: 17 }], labels: [],
}
async function until(check, message, attempts = 100) {
  for (let i = 0; i < attempts; i++) { if (await check()) return; await delay(25) }
  throw new Error(message)
}
async function fixture(browser, { existing = false, slowEncoding = false } = {}) {
  const f = await setup(browser, { logged: true })
  f.page.setDefaultTimeout(4000)
  f.drafts = []; f.sends = []; f.sendCompleted = false
  if (slowEncoding) await f.context.addInitScript(() => {
    const original = File.prototype.arrayBuffer
    window.__encodingPending = 0
    window.__releaseEncoding = null
    File.prototype.arrayBuffer = async function () {
      if (this.name === "invoice.txt") {
        window.__encodingPending++
        await new Promise(resolve => { window.__releaseEncoding = resolve })
      }
      return original.call(this)
    }
  })
  await f.context.route("**/api/**", async route => {
    const req = route.request(), path = new URL(req.url()).pathname
    const respond = body => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    if (path.includes("invoice-attachment")) return route.fulfill({ status: 200, contentType: "text/plain", body: "SYNTHETIC INVOICE" })
    if (path === "/api/mail/messages/fixture-message") return respond({ ...full, ...(existing ? { folder: "Drafts" } : {}) })
    if (existing && path === "/api/mail/messages") return respond({ items: [{ ...full, bodyHtml: undefined, bodyText: undefined, folder: "Drafts" }], totalCount: 1 })
    if (path.startsWith("/api/mail/drafts")) {
      if (req.method() === "POST") {
        const payload = req.postDataJSON(); f.drafts.push({ path, payload })
        return respond({ id: existing ? "fixture-message" : "saved-draft", ...payload })
      }
      return respond({ ok: true })
    }
    if (path === "/api/mail/send") {
      const payload = req.postDataJSON(); f.sends.push(payload)
      if (f.holdSend) await new Promise(resolve => { f.releaseSend = resolve })
      await respond({ id: "sent-id", ...payload }); f.sendCompleted = true; return
    }
    return route.fallback()
  })
  await f.page.goto(base + "/")
  await f.page.locator(".mail-list-row").first().waitFor()
  return f
}
async function openForward(f) {
  await f.page.locator(".mail-list-row").first().click({ button: "right" })
  await f.page.getByRole("menu").getByRole("button", { name: "转发", exact: true }).click()
  await f.page.getByRole("dialog").getByText("invoice.txt", { exact: true }).waitFor()
}
function preserved(payload) {
  assert.equal(payload.attachments?.length, 1, "forwarded attachment absent from saved draft")
  assert.equal(Buffer.from(payload.attachments[0].contentBase64, "base64").toString(), "SYNTHETIC INVOICE")
  assert.ok(payload.html.includes('<table width="600">'), "original table missing")
  assert.ok(payload.html.includes(".invoice-cell{color:#123456}"), "original style block missing")
  assert.ok(payload.html.includes("background:#ffeedd;width:300px"), "original inline style missing")
}
async function scenario(name, run) {
  try { report.push({ name, pass: true, detail: await run() }) }
  catch (error) { report.push({ name, pass: false, error: error.message }) }
}
;(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox"] })
  await scenario("forward-first-autosave", async () => {
    const f = await fixture(browser)
    try {
      await openForward(f)
      await until(() => f.drafts.length > 0, "autosave did not arrive within 8 seconds", 320)
      assert.ok(f.drafts.length, "autosave missing")
      preserved(f.drafts[0].payload)
      assert.deepEqual(f.state.errors, [])
      return { draftCount: f.drafts.length, firstPayload: f.drafts[0].payload }
    } finally { await f.context.close() }
  })
  await scenario("forward-close-saves-original-attachment", async () => {
    const f = await fixture(browser)
    try {
      await openForward(f)
      await f.page.getByRole("button", { name: "取消", exact: true }).click()
      await until(() => f.drafts.length > 0, "close flush missing")
      preserved(f.drafts[0].payload)
      assert.deepEqual(f.state.errors, [])
      return { draftCount: f.drafts.length, firstPayload: f.drafts[0].payload }
    } finally { await f.context.close() }
  })
  await scenario("forward-close-awaits-file-encoding", async () => {
    const f = await fixture(browser, { slowEncoding: true })
    try {
      await openForward(f)
      await f.page.getByRole("button", { name: "取消", exact: true }).click()
      await until(() => f.page.evaluate(() => window.__encodingPending > 0), "file encoding not started")
      assert.equal(f.drafts.length, 0, "draft was saved before attachment encoding completed")
      assert.equal(await f.page.getByRole("dialog").count(), 0)
      await f.page.evaluate(() => window.__releaseEncoding())
      await until(() => f.drafts.length > 0, "close flush did not survive unmount")
      preserved(f.drafts[0].payload)
      assert.deepEqual(f.state.errors, [])
      return { draftCount: f.drafts.length, firstPayload: f.drafts[0].payload }
    } finally { await f.context.close() }
  })
  await scenario("existing-draft-untouched-attachments-preserved", async () => {
    const f = await fixture(browser, { existing: true })
    try {
      await f.page.locator(".mail-list-row").first().click({ button: "right" })
      await f.page.getByRole("menu").getByRole("button", { name: "编辑草稿", exact: true }).click()
      await f.page.getByRole("dialog").getByText("invoice.txt", { exact: true }).waitFor()
      await f.page.locator("input[name=subject]").fill("Existing draft subject edited")
      await f.page.getByRole("button", { name: "取消", exact: true }).click()
      await until(() => f.drafts.length > 0, "existing draft not saved")
      assert.equal(f.drafts[0].path, "/api/mail/drafts/fixture-message")
      assert.ok(!Object.hasOwn(f.drafts[0].payload, "attachments"), "untouched existing attachments overwritten")
      assert.deepEqual(f.state.errors, [])
      return f.drafts[0]
    } finally { await f.context.close() }
  })
  await scenario("old-send-cannot-close-new-unsaved-composer", async () => {
    const f = await fixture(browser)
    try {
      f.holdSend = true
      await f.page.getByRole("button", { name: "写邮件", exact: true }).click()
      await f.page.locator("input[name=to]").fill("recipient@example.test")
      await f.page.locator("input[name=subject]").fill("Message A")
      await f.page.locator(".ProseMirror").fill("Message A body")
      await f.page.getByRole("button", { name: "发送", exact: true }).click()
      await until(() => !!f.releaseSend, "A send not started")
      await f.page.getByRole("button", { name: "取消", exact: true }).click()
      await f.page.getByRole("button", { name: "写邮件", exact: true }).click()
      await f.page.locator("input[name=subject]").fill("UNSAVED Message B")
      await f.page.locator(".ProseMirror").fill("UNSAVED new body B")
      assert.equal(f.drafts.length, 0, "B should still be unsaved at A completion")
      f.releaseSend()
      await until(() => f.sendCompleted, "A did not finish")
      await delay(500)
      assert.equal(await f.page.getByRole("dialog").count(), 1, "A completion closed B")
      assert.equal(await f.page.locator(".ProseMirror").innerText(), "UNSAVED new body B")
      await f.page.getByRole("button", { name: "取消", exact: true }).click()
      await until(() => f.drafts.length > 0, "B was not saved on close")
      assert.equal(f.drafts[0].payload.text, "UNSAVED new body B")
      assert.equal(f.sends.length, 1, "unexpected repeat send")
      assert.deepEqual(f.state.errors, [])
      return { sendCount: f.sends.length, savedB: f.drafts[0].payload }
    } finally { f.releaseSend?.(); await f.context.close() }
  })
  await scenario("current-send-still-closes-its-composer", async () => {
    const f = await fixture(browser)
    try {
      await f.page.getByRole("button", { name: "写邮件", exact: true }).click()
      await f.page.locator("input[name=to]").fill("recipient@example.test")
      await f.page.locator("input[name=subject]").fill("Current message")
      await f.page.locator(".ProseMirror").fill("Current body")
      await f.page.getByRole("button", { name: "发送", exact: true }).click()
      await until(() => f.sendCompleted, "current send did not finish")
      await until(async () => await f.page.getByRole("dialog").count() === 0, "current send did not close its composer")
      assert.equal(f.sends.length, 1)
      assert.deepEqual(f.state.errors, [])
      return { sendCount: f.sends.length, dialogCount: 0 }
    } finally { await f.context.close() }
  })
  await browser.close()
  const output = { pass: report.every(item => item.pass), base, scenarios: report }
  if (process.env.REPORT_PATH) fs.writeFileSync(process.env.REPORT_PATH, JSON.stringify(output, null, 2))
  console.log(JSON.stringify(output, null, 2))
  if (!output.pass) process.exitCode = 1
})().catch(error => { console.error(error); process.exitCode = 1 })
