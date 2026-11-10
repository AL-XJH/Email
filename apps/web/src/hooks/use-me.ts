import { useQuery, type UseQueryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { User } from "@/lib/api"

type MeResponse = { user: User }

export function useMe(
  options?: Omit<UseQueryOptions<MeResponse, Error, MeResponse, ["me"]>, "queryKey" | "queryFn">
) {
  return useQuery({
    queryKey: ["me"],
    queryFn: api.me,
    // 持久化缓存可能带着上一个账号的 me；每次挂载都必须向服务器校验一次，
    // 否则 AuthGuard 既不能相信缓存，也永远等不到 isFetchedAfterMount。
    refetchOnMount: "always",
    retry: 1,
    ...options,
  })
}

export function isTimeoutError(error: unknown): boolean {
  return error instanceof Error && error.message.includes("请求超时")
}
