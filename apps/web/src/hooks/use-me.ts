import { useQuery, type UseQueryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { User, UserPreferences } from "@/lib/api"
import { useAuthSession } from "@/lib/auth-session"
import { ApiError } from "@/lib/ui-errors"

type MeResponse = { user: User; preferences?: UserPreferences }

export function useMe(
  options?: Omit<UseQueryOptions<MeResponse, Error, MeResponse, ["me"]>, "queryKey" | "queryFn">
) {
  const session = useAuthSession()
  const query = useQuery({
    queryKey: ["me"],
    queryFn: api.me,
    // 持久化缓存可能带着上一个账号的 me；每次挂载都必须向服务器校验一次，
    // 否则 AuthGuard 既不能相信缓存，也永远等不到 isFetchedAfterMount。
    refetchOnMount: "always",
    retry: (failures, error) =>
      !(error instanceof ApiError && error.status === 401) && failures < 1,
    ...options,
    enabled: !session.expired && options?.enabled,
  })
  // React Query retains successful data after a failed refetch; never expose it
  // to auth consumers after an error or a session-wide cache reset.
  return {
    ...query,
    data: query.isError || session.expired ? undefined : query.data,
    isUnauthorized: session.expired,
  }
}

export function isTimeoutError(error: unknown): boolean {
  return error instanceof Error && error.message.includes("请求超时")
}
