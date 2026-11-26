// Shell geometry regression: the fixed sidebar must reserve its own width so
// page content never renders underneath it. Tailwind 4 silently drops the v3
// `w-[--var]` shorthand, which collapsed the gap to 0 and hid the left column
// of every admin/mail page; assert real boxes, not just that pages load.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const assert = require('node:assert/strict')
const base = process.env.BASE_URL || 'http://127.0.0.1:18089'
const permissions = [
  'admin.settings.view', 'admin.settings.update', 'admin.templates.view',
  'admin.users.view', 'admin.users.create', 'admin.users.update', 'admin.users.delete',
  'admin.permissions.view', 'admin.domains.view', 'admin.mailboxes.view', 'admin.aliases.view',
  'admin.messages.view', 'admin.backups.view', 'mail.access', 'mail.messages.read',
]
const SIDEBAR = 256 // 16rem

async function fixture(browser, width) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } })
  const errors = []
  await ctx.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    const p = url.pathname
    if (p === '/api/events') return route.abort()
    let body = { items: [] }
    if (p === '/api/me') body = { user: { id: 'u', role: 'admin', email: 'admin@example.test', displayName: 'A', permissions, permissionGroupIds: [], permissionGroups: [], disabled: false, protected: true, twoFactorEnabled: false, mailboxCount: 0, mailboxQuotaBonus: 0, mailboxesCreatedTotal: 0, createdAt: '2026-01-01T00:00:00Z', limits: {} }, preferences: {} }
    if (p === '/api/public/settings') body = { siteTitle: '回归测试' }
    if (p === '/api/admin/settings') body = { siteTitle: '回归测试', publicHostname: 'mail.example.test', publicBaseUrl: 'http://example.test', sessionTtlHours: 168, maildirScanSeconds: 5, allowInsecureHttp: false }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
  const p = await ctx.newPage()
  p.on('pageerror', e => errors.push(e.message))
  return { ctx, p, errors }
}

const measure = p => p.evaluate(() => {
  const rect = el => { const b = el.getBoundingClientRect(); return { x: Math.round(b.x), w: Math.round(b.width) } }
  const fixed = [...document.querySelectorAll('div')].filter(d => getComputedStyle(d).position === 'fixed' && Math.round(d.getBoundingClientRect().width) > 8)
  const side = fixed.find(d => d.className.includes('sidebar'))
  const main = document.querySelector('main')
  const tab = [...document.querySelectorAll('button')].find(b => b.textContent.trim() === '基础')
  return { side: side ? rect(side) : null, main: main ? rect(main) : null, tab: tab ? rect(tab) : null }
})

;(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ['--no-sandbox'] })
  const results = []
  const run = async (name, width, fn) => {
    let f
    try { f = await fixture(browser, width); await fn(f); assert.deepEqual(f.errors, []); results.push({ name, pass: true }) }
    catch (e) { results.push({ name, pass: false, error: e.message }) }
    finally { await f?.ctx.close(); console.log(JSON.stringify(results.at(-1))) }
  }
  for (const width of [1440, 1280, 1024]) {
    await run(`admin-shell-reserves-sidebar-${width}`, width, async ({ p }) => {
      await p.goto(base + '/admin?section=settings', { waitUntil: 'networkidle', timeout: 30000 })
      await p.getByRole('button', { name: '基础', exact: true }).waitFor()
      const m = await measure(p)
      assert.equal(m.side?.w, SIDEBAR, 'sidebar width must resolve from --sidebar-width')
      assert.ok(m.main.x >= SIDEBAR, `main starts at ${m.main.x}, under the ${SIDEBAR}px sidebar`)
      assert.ok(m.tab.x >= SIDEBAR, `first tab at ${m.tab.x} is hidden under the sidebar`)
    })
  }
  await run('mobile-shell-has-no-offset-700', 700, async ({ p }) => {
    await p.goto(base + '/admin?section=settings', { waitUntil: 'networkidle', timeout: 30000 })
    await p.getByRole('button', { name: '基础', exact: true }).waitFor()
    const m = await measure(p)
    assert.ok(!m.side || m.side.w < 8, 'mobile must not render a fixed sidebar')
    assert.equal(m.main.x, 0)
    assert.ok(m.tab.x < 60, 'mobile content starts at the viewport edge')
  })
  await browser.close()
  console.log(`${results.filter(r => r.pass).length}/${results.length} passed`)
  if (results.some(r => !r.pass)) process.exitCode = 1
})().catch(e => { console.error(e); process.exitCode = 1 })
