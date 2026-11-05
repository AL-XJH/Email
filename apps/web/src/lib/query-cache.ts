import { dehydrate, hydrate, type QueryClient } from "@tanstack/react-query"

// 刷新页面时，React Query 的内存缓存会清空，于是一瞬间全是骨架屏：
// 侧栏的文件夹/标签变成灰条、邮件列表变成灰行，等接口回来才填上。
// 这里把成功过的查询存进 localStorage，下次启动先 hydrate 回内存，
// 页面立刻有内容，接口回来再静默替换。
const CACHE_KEY = "lanqin:query-cache"
const MAX_AGE_MS = 24 * 60 * 60 * 1000

type Snapshot = { at: number; state: ReturnType<typeof dehydrate> }

export function restoreQueryCache(client: QueryClient) {
  try {
    const raw = window.localStorage.getItem(CACHE_KEY)
    if (!raw) return
    const snapshot = JSON.parse(raw) as Snapshot
    if (!snapshot?.at || Date.now() - snapshot.at > MAX_AGE_MS) {
      window.localStorage.removeItem(CACHE_KEY)
      return
    }
    hydrate(client, snapshot.state)
  } catch {
    // 缓存坏了就当没有，别让启动挂掉
    try {
      window.localStorage.removeItem(CACHE_KEY)
    } catch {
      /* ignore */
    }
  }
}

export function persistQueryCache(client: QueryClient) {
  let timer: number | undefined
  const write = () => {
    try {
      const state = dehydrate(client, {
        shouldDehydrateQuery: (query) => query.state.status === "success",
      })
      window.localStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), state }))
    } catch {
      // 隐私模式 / 配额满：放弃持久化，功能不受影响
    }
  }
  const schedule = () => {
    window.clearTimeout(timer)
    timer = window.setTimeout(write, 800)
  }
  client.getQueryCache().subscribe(schedule)
  window.addEventListener("pagehide", write)
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") write()
  })
}

export function clearQueryCacheSnapshot() {
  try {
    window.localStorage.removeItem(CACHE_KEY)
  } catch {
    /* ignore */
  }
}
