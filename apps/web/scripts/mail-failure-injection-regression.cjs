// 前端失败注入回归：接口失败时用户看到什么、数据留成什么样。
//
// 覆盖三条只在对方接口出错时才会走到的路径：
//   1) 批量操作部分失败 —— 必须提示「已处理 N 封，M 封失败」，失败的邮件留在列表里；
//   2) 移动失败 —— 邮件不能从列表里消失（没有乐观删除），要弹「移动失败」；
//   3) 添加标签失败 —— 标签不能留在邮件上（缓存要回滚），要弹「添加标签失败」。
//
// 全部走 mock API，不碰真实邮箱。
const assert = require('node:assert/strict')
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')

const base = process.env.BASE_URL || 'http://127.0.0.1:18096'
const MB = 'mbx_failure'
const A = 'mail_failure_a'
const B = 'mail_failure_b'
const permissions = [
  'mail.read', 'mail.send', 'mail.manage', 'mail.organize', 'mail.settings',
  'admin.users', 'admin.settings',
]

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function row(id, subject, receivedAt, extra = {}) {
  return {
    id, subject, snippet: subject, size: 1024,
    from: 'Sender <sender@example.test>',
    to: ['a@example.test'], cc: [],
    receivedAt, isStarred: false, flags: { seen: true, flagged: false },
    hasAttachments: false, ...extra,
  }
}

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ['--no-sandbox'],
  })
  const results = []
  const run = async (name, fn) => {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    const errors = []
    const state = { inbox: [row(A, 'alpha', '2026-01-02T00:00:00Z'), row(B, 'beta', '2026-01-01T00:00:00Z')], labels: [] }
    const bulkCalls = []
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
      if (p === '/api/mail/folders') return json(200, [])
      if (p === '/api/mail/labels') return json(200, { items: [{ id: 'lbl_1', name: '工作', color: '#2563eb' }] })
      if (['/api/mail/scheduled-sends', '/api/mail/send-queue'].includes(p)) return json(200, [])
      if (p === '/api/me/stats') return json(200, { unread: 0, total: state.inbox.length, storageUsed: 0, storageQuota: 0 })
      if (p === '/api/mail/messages') return json(200, { items: state.inbox, total: state.inbox.length, page: 1, pageSize: 30 })
      if (p === '/api/mail/messages/bulk') {
        bulkCalls.push(JSON.parse(req.postData() || '{}'))
        return json(200, { ok: true, updated: 1, failed: [B], missing: [] })
      }
      if (p.endsWith('/move')) return json(500, { error: 'move exploded' })
      if (p.endsWith('/labels')) return json(500, { error: 'label exploded' })
      if (p.startsWith('/api/mail/messages/')) {
        const id = p.split('/').pop()
        const item = state.inbox.find((m) => m.id === id)
        return json(200, {
          ...(item || row(id, 'x', '2026-01-01T00:00:00Z')),
          bodyHtml: '<p>hi</p>', bodyText: 'hi', attachments: [], labels: item?.labels || [],
        })
      }
      return json(200, { items: [] })
    })
    const page = await ctx.newPage()
    page.on('pageerror', (e) => errors.push(e.message))
    try {
      await fn({ page, state, bulkCalls })
      assert.deepEqual(errors, [])
      results.push({ name, pass: true })
    } catch (e) {
      results.push({ name, pass: false, error: e.message })
    } finally {
      await ctx.close()
    }
    console.log(JSON.stringify(results.at(-1)))
  }

  const open = async (page) => {
    await page.goto(`${base}/?mailboxId=${MB}`, { waitUntil: 'domcontentloaded', timeout: 30000 })
    await page.waitForSelector('button[aria-label="选择邮件"]', { timeout: 20000 })
  }

  // 1) 批量操作部分失败
  await run('bulk-partial-failure-reports-counts', async ({ page, state, bulkCalls }) => {
    await open(page)
    const checks = page.locator('button[aria-label="选择邮件"]')
    assert.equal(await checks.count(), 2, 'fixture must render two rows')
    await checks.nth(0).dispatchEvent('click')
    await checks.nth(1).dispatchEvent('click')
    await page.getByRole('button', { name: '批量操作' }).click()
    await page.getByRole('menuitem', { name: '标为已读' }).click()
    // 失败的那封不能从列表里消失
    await page.waitForFunction(
      (subject) => document.body.innerText.includes(subject),
      'beta',
      { timeout: 5000 }
    )
    const toast = await page.waitForFunction(
      () => document.body.innerText.includes('封失败'),
      null,
      { timeout: 5000 }
    ).then((h) => h.jsonValue())
    assert.ok(toast, 'toast must mention how many items failed')
    assert.equal(bulkCalls.length, 1, '批量操作应当一次请求处理整批')
    assert.equal(bulkCalls[0].ids.length, 2, 'both selected ids should be sent in one request')
    assert.equal(state.inbox.length, 2, 'failed item must stay in the list')
  })

  // 2) 移动失败：邮件不能消失
  await run('move-failure-keeps-message-and-toasts', async ({ page, state }) => {
    await open(page)
    await page.getByRole('button', { name: 'Sender alpha' }).first().click()
    const archive = page.getByRole('button', { name: '归档', exact: true }).first()
    await archive.waitFor({ timeout: 10000 })
    await archive.click()
    await sleep(400)
    const stillThere = await page.getByRole('button', { name: 'Sender alpha' }).count()
    assert.ok(stillThere > 0, '移动失败后邮件仍应留在列表里')
    const toasted = await page.evaluate(() => document.body.innerText.includes('移动失败'))
    assert.ok(toasted, '应当提示移动失败')
    assert.equal(state.inbox.length, 2, 'mock list must be unchanged')
  })

  // 3) 添加标签失败：标签不能留在邮件上
  await run('label-failure-rolls-back-chip', async ({ page }) => {
    await open(page)
    await page.getByRole('button', { name: 'Sender alpha' }).first().click()
    const labelTrigger = page.getByRole('button', { name: '添加标签' }).first()
    await labelTrigger.waitFor({ timeout: 10000 })
    await labelTrigger.click()
    const labelItem = page.getByRole('menuitemcheckbox', { name: /工作/ }).first()
    await labelItem.waitFor({ timeout: 5000 })
    await labelItem.click()
    const toasted = await page.waitForFunction(
      () => document.body.innerText.includes('添加标签失败'),
      null,
      { timeout: 5000 }
    ).then(() => true)
    assert.ok(toasted, '应当提示添加标签失败')
    // 回滚的直接证据：菜单还开着，这一项必须仍未勾选（0->optimistic true->回滚 false）。
    const checked = await labelItem.getAttribute('aria-checked')
    assert.notEqual(checked, 'true', '接口失败后标签不能留在邮件上（复选框仍被勾选）')
  })

  await browser.close()
  console.log(`${results.filter((r) => r.pass).length}/${results.length} passed`)
  if (results.some((r) => !r.pass)) process.exitCode = 1
}

main().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
