import { errorMessage } from "@/lib/ui-errors"
import React from "react"
import { Navigate, useLocation } from "react-router-dom"
import { useMe, isTimeoutError } from "@/hooks/use-me"
import { AuthLoading, AuthError } from "@/components/auth-states"

export function AuthGuard({ children }: { children: React.ReactNode }) {
  const me = useMe()
  const location = useLocation()

  // 持久化缓存里的 ["me"] 会让 isLoading=false；必须等本次挂载后的
  // /api/me 返回，否则上一个账号的邮箱/邮件会在鉴权前先渲染出来。
  if (me.isLoading || !me.isFetchedAfterMount) return <AuthLoading />
  if (me.isError && isTimeoutError(me.error))
    return <AuthError message={errorMessage(me.error)} onRetry={() => me.refetch()} />
  if (me.isError || !me.data?.user) {
    return (
      <Navigate
        to="/login"
        replace
        state={{ from: `${location.pathname}${location.search}${location.hash}` }}
      />
    )
  }

  return <>{children}</>
}
