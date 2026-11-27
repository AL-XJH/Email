// 草稿自动保存不能每次都把附件重传一遍。
//
// 现状（修复前）：只要用户动过附件，attachmentsTouched 一直是 true；
// 之后每改一个字，自动保存都会把所有附件重新 base64 编码并整包 POST。
// 10 MB 附件 + 5 秒防抖 = 每敲一句就传十几 MB（国内到美国线路尤其致命）。
//
// 断言：第一次保存带 attachments；之后只改正文的保存，请求体里不能再出现
// attachments 字段（服务端会保留已有附件）。
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')

const base = process.env.BASE_URL || 'http://127.0.0.1:18099'
const MB = 'mbx_draft'
const permissions = ['mail.read', 'mail.send', 'mail.manage', 'mail.organize', 'mail.settings']

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ['--no-sandbox'],
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const errors = []
  const draftSaves = []

  await ctx.route('**/api/**', async (route) => {
    const req = route.request()
    const p = new URL(req.url()).pathname
    const json = (status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
    if (p === '/api/events') return route.abort()
    if (p === '/api/me') {
      return json(200, {
        user: {
          id: 'u', role: 'admin', email: 'admin@example.test', displayName: 'A',
          permissions, permissionGroupIds: [], permissionGroups: [], disabled: false,
          protected: true, twoFactorEnabled: false, mailboxCount: 1,
          mailboxQuotaBonus: 0, mailboxesCreatedTotal: 1,
          createdAt: '2026-01-01T00:00:00Z', limits: {},
        },
        preferences: { showRemoteImages: true },
      })
    }
    if (p === '/api/public/settings') return json(200, { siteTitle: '回归' })
    if (p === '/api/mail/mailboxes') {
      return json(200, { items: [{ id: MB, address: 'a@example.test', displayName: 'A', isPrimary: true, quotaBytes: 0, usedBytes: 0 }] })
    }
    if (p === '/api/mail/folders') return json(200, { items: [] })
    if (p === '/api/mail/labels') return json(200, { items: [] })
    if (['/api/mail/scheduled-sends', '/api/mail/send-queue'].includes(p)) return json(200, [])
    if (p === '/api/me/stats') return json(200, { unread: 0, total: 0, storageUsed: 0, storageQuota: 0 })
    if (p === '/api/mail/messages') return json(200, { items: [], total: 0, page: 1, pageSize: 30 })
    if (p === '/api/mail/drafts' || p.startsWith('/api/mail/drafts/')) {
      const body = JSON.parse(req.postData() || '{}')
      draftSaves.push({ url: p, body })
      const id = p.includes('/api/mail/drafts/') ? p.split('/').pop() : 'drf_1'
      return json(200, {
        id, subject: body.subject || '', to: body.to || [], cc: [], bcc: [],
        bodyHtml: body.html || '', snippet: '', receivedAt: '2026-01-01T00:00:00Z',
        isRead: true, isStarred: false, hasAttachments: Boolean(body.attachments?.length),
        attachments: body.attachments || [],
      })
    }
    return json(200, { items: [] })
  })

  const page = await ctx.newPage()
  page.on('pageerror', (e) => errors.push(e.message))

  const results = []
  const run = async (name, fn) => {
    try {
      await fn()
      assert.deepEqual(errors, [])
      results.push({ name, pass: true })
    } catch (e) {
      results.push({ name, pass: false, error: e.message })
    }
    console.log(JSON.stringify(results.at(-1)))
  }

  await page.goto(`${base}/?mailboxId=${MB}`, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.getByRole('button', { name: '写邮件' }).first().click()
  const subjectInput = page.getByPlaceholder('输入主题')
  await subjectInput.waitFor({ timeout: 15000 })
  await subjectInput.fill('附件草稿')

  const tmpFile = path.join(os.tmpdir(), 'draft-attach.bin')
  fs.writeFileSync(tmpFile, Buffer.alloc(64 * 1024, 7))
  const fileInput = page.locator('input[type="file"]').first()
  if (!(await fileInput.count())) throw new Error('composer has no file input')
  await fileInput.setInputFiles(tmpFile)

  await run('draft-save-sends-attachments-only-once', async () => {
    // 第一次保存：必须带附件
    await page.waitForFunction(() => true)
    const deadline = Date.now() + 20000
    while (Date.now() < deadline && draftSaves.length === 0) await sleep(300)
    assert.ok(draftSaves.length >= 1, '没有触发草稿保存')
    assert.ok(
      Array.isArray(draftSaves[0].body.attachments) && draftSaves[0].body.attachments.length === 1,
      `第一次保存应带 1 个附件，实际 ${JSON.stringify(draftSaves[0].body.attachments || null)}`
    )

    // 之后只改正文
    const editor = page.locator('[contenteditable="true"]').first()
    await editor.waitFor({ timeout: 10000 })
    await editor.click()
    await page.keyboard.type('只改正文，不碰附件')
    const before = draftSaves.length
    const deadline2 = Date.now() + 25000
    while (Date.now() < deadline2 && draftSaves.length === before) await sleep(300)
    assert.ok(draftSaves.length > before, '改正文后没有触发新的保存')
    const second = draftSaves[draftSaves.length - 1].body
    assert.ok(
      second.attachments === undefined || second.attachments.length === 0,
      `只改正文的保存又带了 ${second.attachments?.length ?? 0} 个附件（应当省略，让服务端保留原件）`
    )
  })

  await ctx.close()
  await browser.close()
  fs.rmSync(tmpFile, { force: true })
  console.log(`${results.filter((r) => r.pass).length}/${results.length} passed`)
  if (results.some((r) => !r.pass)) process.exitCode = 1
}

main().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
