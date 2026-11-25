// Runs against the actual TypeScript modules with the locked TypeScript 6 API.
const assert = require("node:assert/strict")
const fs = require("node:fs")
const { createRequire } = require("node:module")
const source = process.env.WEB_DIR || "/work/apps/web"
const projectRequire = createRequire(`${source}/package.json`)
const ts = projectRequire("typescript")
require.extensions[".ts"] = (module, filename) => {
  const { outputText } = ts.transpileModule(fs.readFileSync(filename, "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  })
  module._compile(outputText, filename)
}
const storage = new Map()
global.window = {
  localStorage: {
    getItem: (key) => storage.get(key) || null,
    setItem: (key, value) => storage.set(key, value),
    removeItem: (key) => storage.delete(key),
  },
  setTimeout,
  clearTimeout,
}
const { QueryClient, QueryObserver } = projectRequire("@tanstack/react-query")
const { api, setUnauthorizedHandler } = require(`${source}/src/lib/api.ts`)
const auth = require(`${source}/src/lib/auth-session.ts`)
const { QUERY_CACHE_KEY } = require(`${source}/src/lib/query-cache.ts`)
const response = (status, data) =>
  new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } })
const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
let clears = 0
setUnauthorizedHandler(() => {
  clears++
  client.clear()
})
const seed = () => {
  client.setQueryData(["mailboxes"], { secret: "old-account" })
  storage.set(QUERY_CACHE_KEY, "old-account")
}
;(async () => {
  seed()
  const pending = []
  global.fetch = () => new Promise((resolve) => pending.push(resolve))
  const first = api.me().catch((e) => e)
  const concurrent = api.mailboxes().catch((e) => e)
  const generation = auth.authSessionGeneration()
  pending[0](response(401, { error: "unauthorized" }))
  await first
  assert.equal(clears, 1)
  assert.equal(client.getQueryData(["mailboxes"]), undefined)
  assert.equal(storage.get(QUERY_CACHE_KEY), undefined)
  assert.ok(auth.authSessionGeneration() > generation)
  pending[1](response(401, { error: "unauthorized" }))
  await concurrent
  assert.equal(clears, 1, "parallel 401 must clear once per session")

  auth.startAuthSession(client)
  seed()
  const currentGeneration = auth.authSessionGeneration()
  const old401 = api.me().catch((e) => e)
  const old200 = api.mailboxes().catch((e) => e)
  auth.startAuthSession(client)
  client.setQueryData(["mailboxes"], { account: "new-account" })
  pending[2](response(401, { error: "unauthorized" }))
  pending[3](response(200, { items: [{ secret: "old-account" }] }))
  const [error401, error200] = await Promise.all([old401, old200])
  assert.equal(error401.status, 401)
  assert.equal(error200.status, 401, "late successful data must not cross session boundary")
  assert.equal(clears, 1, "old generation 401 cannot clear the new account")
  assert.deepEqual(client.getQueryData(["mailboxes"]), { account: "new-account" })
  assert.ok(auth.authSessionGeneration() > currentGeneration)

  global.fetch = async () => response(401, { error: "bad credentials" })
  const beforeLoginError = auth.authSessionGeneration()
  await api.login({ email: "synthetic@example.test", password: "synthetic" }).catch(() => {})
  assert.equal(auth.authSessionGeneration(), beforeLoginError, "login 401 is a form error")
  assert.equal(clears, 1)

  // Observe the production failure mechanism directly with a real QueryObserver.
  auth.startAuthSession(client)
  global.fetch = async () => response(401, { error: "unauthorized" })
  const observer = new QueryObserver(client, { queryKey: ["me"], queryFn: api.me, retry: false })
  const unsubscribe = observer.subscribe(() => {})
  await new Promise((resolve) => setTimeout(resolve, 30))
  assert.equal(client.getQueryData(["me"]), undefined)
  // The observer may remain pending after clear; the independent session signal
  // is what makes AuthGuard leave the skeleton. Browser tests assert the redirect.
  assert.equal(clears, 2)
  unsubscribe()
  client.clear()
  console.log(
    JSON.stringify({
      pass: true,
      assertions: [
        "401 clears memory and snapshot",
        "parallel 401 deduplicated",
        "old 401 cannot expire new login",
        "old 200 rejected",
        "bad login preserves auth generation",
        "query removal retains independent session state",
      ],
      clears,
      observerFetchedAfterMount: observer.getCurrentResult().isFetchedAfterMount,
    })
  )
})().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
