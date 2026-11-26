// Remote-image regression: `<iframe referrerPolicy>` is ignored for srcdoc
// documents, so Chrome sent `Referer: https://<host>/` for every image inside a
// message. Senders with hotlink protection (Trakt's CDN) answer 403 and the
// logo disappears. The frame document itself must carry
// `<meta name="referrer" content="no-referrer">`.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const assert = require('node:assert/strict')
const https = require('node:https')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const base = process.env.BASE_URL || 'http://127.0.0.1:18089'
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64'
)

function hotlinkServer() {
  // The reader page ships `img-src 'self' data: blob: https:` — the probe has to
  // be https or the site CSP (inherited by the srcdoc frame) blocks it first.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mail-referrer-'))
  const key = path.join(dir, 'key.pem')
  const cert = path.join(dir, 'cert.pem')
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-subj', '/CN=127.0.0.1', '-keyout', key, '-out', cert], { stdio: 'ignore' })
  const seen = []
  const server = https.createServer({ key: fs.readFileSync(key), cert: fs.readFileSync(cert) }, (req, res) => {
    seen.push({ url: req.url, referer: req.headers.referer || '' })
    if (req.headers.referer) {
      res.writeHead(403, { 'content-type': 'text/plain' })
      res.end('hotlinking not allowed')
      return
    }
    res.writeHead(200, { 'content-type': 'image/png', 'content-length': PNG.length })
    res.end(PNG)
  })
  return new Promise(resolve => server.listen(0, '127.0.0.1', () => resolve({ server, seen, port: server.address().port })))
}

const permissions = [
  'mail.access', 'mail.messages.read', 'mail.messages.send', 'mail.messages.delete',
]
const messageId = 'mail_regression_referrer'

async function fixture(browser, imageUrl) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true })
  const errors = []
  await ctx.route('**/api/**', async route => {
    const p = new URL(route.request().url()).pathname
    if (p === '/api/events') return route.abort()
    let body = { items: [] }
    if (p === '/api/me') body = { user: { id: 'u', role: 'admin', email: 'admin@example.test', displayName: 'A', permissions, permissionGroupIds: [], permissionGroups: [], disabled: false, protected: true, twoFactorEnabled: false, mailboxCount: 1, mailboxQuotaBonus: 0, mailboxesCreatedTotal: 1, createdAt: '2026-01-01T00:00:00Z', limits: {} }, preferences: { showRemoteImages: true } }
    if (p === '/api/public/settings') body = { siteTitle: '回归测试' }
    if (p === '/api/mail/mailboxes') body = { items: [{ id: 'mbx_regression', address: 'a@example.test', displayName: 'A', isPrimary: true, quotaBytes: 0, usedBytes: 0 }] }
    if (['/api/mail/folders', '/api/mail/labels', '/api/mail/scheduled-sends', '/api/mail/send-queue'].includes(p)) body = []
    if (p === '/api/me/stats') body = { unread: 0, total: 0, storageUsed: 0, storageQuota: 0 }
    if (p === '/api/mail/messages') body = {
      items: [{
        id: messageId, subject: 'remote image', snippet: 'hello', size: 1024,
        from: { address: 'sender@example.test', name: 'Sender' },
        to: [{ address: 'a@example.test' }], cc: [], receivedAt: '2026-01-01T00:00:00Z',
        flags: { seen: false, flagged: false }, hasAttachments: false,
      }],
      total: 1, page: 1, pageSize: 30,
    }
    if (p === '/api/mail/messages') body = {
      items: [{
        id: messageId, subject: 'remote image', snippet: 'hello', size: 1024,
        from: 'Sender <sender@example.test>', to: ['a@example.test'], cc: [],
        receivedAt: '2026-01-01T00:00:00Z', flags: { seen: false, flagged: false }, hasAttachments: false,
      }],
      total: 1, page: 1, pageSize: 30,
    }
    if (p === `/api/mail/messages/${messageId}`) body = {
      id: messageId,
      subject: 'remote image',
      bodyHtml: `<p>hello</p><img id="probe" src="${imageUrl}" width="120" height="40" alt="probe">`,
      bodyText: 'hello',
      attachments: [],
      to: ['a@example.test'],
      cc: [],
      from: 'Sender <sender@example.test>',
      receivedAt: '2026-01-01T00:00:00Z',
      flags: {},
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
  const p = await ctx.newPage()
  p.on('pageerror', e => errors.push(e.message))
  return { ctx, p, errors }
}

;(async () => {
  const { server, seen, port } = await hotlinkServer()
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ['--no-sandbox'] })
  const results = []
  const run = async (name, fn) => {
    let f
    try {
      f = await fixture(browser, `https://127.0.0.1:${port}/logo.png`)
      await fn(f)
      assert.deepEqual(f.errors, [])
      results.push({ name, pass: true })
    } catch (e) {
      results.push({ name, pass: false, error: e.message })
    } finally {
      await f?.ctx.close()
      console.log(JSON.stringify(results.at(-1)))
    }
  }

  await run('srcdoc-drops-referer-for-remote-images', async ({ p }) => {
    await p.goto(`${base}/?mailboxId=mbx_regression&messageId=${messageId}`, { waitUntil: 'domcontentloaded', timeout: 30000 })
    const frameEl = await p.waitForFunction(() => document.querySelector('iframe'), null, { timeout: 20000 }).then(h => h.asElement())
    const frame = await frameEl.contentFrame()
    await frame.waitForSelector('#probe', { timeout: 15000 })
    const meta = await frame.evaluate(() => document.querySelector('meta[name="referrer"]')?.content || null)
    assert.equal(meta, 'no-referrer', 'frame document must declare <meta name="referrer" content="no-referrer">')
    await p.waitForFunction(
      () => { const f = document.querySelector('iframe'); const d = f && f.contentDocument; const i = d && d.getElementById('probe'); return !!i && i.complete },
      null,
      { timeout: 15000 }
    )
    const loaded = await frame.evaluate(() => { const i = document.getElementById('probe'); return { w: i.naturalWidth, h: i.naturalHeight } })
    assert.ok(loaded.w > 0, `remote image did not load (naturalWidth ${loaded.w})`)
    assert.ok(seen.length > 0, 'probe server received no request')
    // Chrome's preload scanner can fire one speculative request before it parses
    // the frame's referrer meta; what matters is that the request the image
    // actually resolves with carries no Referer (otherwise a hotlink-protected
    // sender answers 403 forever).
    const accepted = seen.filter(s => !s.referer)
    assert.ok(accepted.length > 0, `every request carried a Referer: ${JSON.stringify(seen)}`)
    const docReferrer = await frame.evaluate(() => document.referrer)
    assert.equal(docReferrer, '', 'frame document must report an empty referrer')
  })

  await browser.close()
  server.close()
  console.log(`${results.filter(r => r.pass).length}/${results.length} passed`)
  if (results.some(r => !r.pass)) process.exitCode = 1
})().catch(e => { console.error(e); process.exitCode = 1 })
