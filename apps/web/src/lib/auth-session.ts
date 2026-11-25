import { useSyncExternalStore } from "react"
import type { QueryClient } from "@tanstack/react-query"
import { clearQueryCacheSnapshot } from "./query-cache"

// Authentication must survive removal of the query that discovered the 401.
let session = { generation: 0, expired: false }
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => listeners.delete(listener)
}
const snapshot = () => session
const notify = () => listeners.forEach((listener) => listener())

export function useAuthSession() {
  return useSyncExternalStore(subscribe, snapshot)
}

export function authSessionGeneration() {
  return session.generation
}

export function expireAuthSession(generation: number, clearCache: () => void) {
  // A delayed response from a previous account must not expire the new session.
  if (generation !== session.generation || session.expired) return
  session = { generation: generation + 1, expired: true }
  clearCache()
  clearQueryCacheSnapshot()
  notify()
}

export function startAuthSession(client: QueryClient) {
  // Remove both persisted and in-flight data before observing the new account.
  session = { generation: session.generation + 1, expired: false }
  client.clear()
  clearQueryCacheSnapshot()
  notify()
}
