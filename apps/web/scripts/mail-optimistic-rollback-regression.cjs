// 回归：一封邮件的操作失败，不能把同一时间别的邮件的乐观更新一起回滚。
//
// 背景：详情/列表的乐观更新曾经共用一次整体快照回滚（restoreMessageCaches），
// 弱网下连点两个星标、其中一个请求 500，另一个已经成功的星标会从界面上"跳回去"，
// 直到列表重新拉取才恢复。这里把 A 的请求做成快速失败、B 的请求做成慢成功，
// 在 B 还没回来之前断言 B 仍是已星标——旧实现会在这个窗口里丢掉 B 的星标。
//
// 用法同其它脚本：BASE_URL 指向已构建的前端（后端不需要在跑，/api 全部 mock）。
const assert = require('node:assert/strict')
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')

const base = process.env.BASE_URL || 'http://127.0.0.1:18096'
const MB = 'mbx_regression'
const slow = 'mail_optimistic_slow'
const fast = 'mail_optimistic_fast'
const permissions = [
  'mail.read', 'mail.send', 'mail.manage', 'mail.organize', 'mail.settings',
  'admin.users', 'admin.settings',
]

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function row(id, subject, receivedAt, starred) {
  return {
    id, subject, snippet: subject, size: 1024,
    from: 'Sender <sender@example.test>',
    to: ['a@example.test'], cc: [],
    receivedAt, isStarred: starred.has(id), flags: { seen: true, flagged: starred.has(id) }, hasAttachments: false,
  }
}

async function main() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ['--no-sandbox'],
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const errors = []
  const starred = new Set()

  await ctx.route('**/api/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const p = url.pathname
    const json = (status, body) =>
      route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

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
    if (p === '/api/public/settings') return json(200, { siteTitle: '回归测试' })
    if (p === '/api/mail/mailboxes') {
      return json(200, {
        items: [{ id: MB, address: 'a@example.test', displayName: 'A', isPrimary: true, quotaBytes: 0, usedBytes: 0 }],
      })
    }
    if (['/api/mail/folders', '/api/mail/labels', '/api/mail/scheduled-sends', '/api/mail/send-queue'].includes(p)) {
      return json(200, [])
    }
    if (p === '/api/me/stats') {
      return json(200, { unread: 0, total: 2, storageUsed: 0, storageQuota: 0 })
    }
    if (p === '/api/mail/messages') {
      return json(200, {
        items: [
          row(slow, 'beta slow', '2026-01-02T00:00:00Z', starred),
          row(fast, 'alpha fast', '2026-01-01T00:00:00Z', starred),
        ],
        total: 2, page: 1, pageSize: 30,
      })
    }
    if (p.startsWith('/api/mail/messages/') && p.endsWith('/star')) {
      const id = p.split('/').slice(-2)[0]
      const body = JSON.parse(req.postData() || '{}')
      if (id === fast) {
        // 快速失败：模拟弱网下一次 500
        await sleep(120)
        return json(500, { error: 'star failed' })
      }
      // 慢成功：A 的失败必须发生在 B 还在飞行中的窗口里
      await sleep(1600)
      if (body.starred) starred.add(id)
      else starred.delete(id)
      return json(200, { ok: true })
    }
    if (p.startsWith('/api/mail/messages/')) {
      const id = p.split('/').pop()
      return json(200, {
        id, subject: id === slow ? 'beta slow' : 'alpha fast',
        bodyHtml: '<p>hi</p>', bodyText: 'hi', attachments: [],
        to: ['a@example.test'], cc: [], from: 'Sender <sender@example.test>',
        receivedAt: '2026-01-01T00:00:00Z', isStarred: starred.has(id), flags: { seen: true, flagged: starred.has(id) },
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
  await page.waitForSelector('button[aria-label="添加星标"]', { timeout: 20000 })

  await run('failed-star-keeps-other-optimistic-star', async () => {
    const stars = page.locator('button[aria-label="添加星标"]')
    assert.equal(await stars.count(), 2, 'fixture must render two unstarred rows')

    // 第 0 行 = beta(slow, 会成功)，第 1 行 = alpha(fast, 会 500)
    await stars.nth(1).dispatchEvent('click')
    await stars.nth(0).dispatchEvent('click')

    await page.waitForFunction(
      () => document.querySelectorAll('button[aria-label="取消星标"]').length >= 2,
      null,
      { timeout: 3000 }
    )

    // alpha 的 500 已经在路上/已返回，beta 的请求还没结束。
    await sleep(700)
    const stillStarred = await page.locator('button[aria-label="取消星标"]').count()
    assert.equal(
      stillStarred, 1,
      `失败的那封回滚后，成功的星标不该跟着消失（当前可见已星标 ${stillStarred} 个，期望 1 个）`
    )

    // beta 成功后列表会重拉；最终状态应是 1 个星标。
    await sleep(2200)
    const finalStarred = await page.locator('button[aria-label="取消星标"]').count()
    assert.equal(finalStarred, 1, `最终应只剩 1 个星标，实际 ${finalStarred}`)
  })

  await ctx.close()
  await browser.close()
  console.log(`${results.filter((r) => r.pass).length}/${results.length} passed`)
  if (results.some((r) => !r.pass)) process.exitCode = 1
}

main().catch((e) => {
  console.error(e)
  process.exitCode = 1
})
