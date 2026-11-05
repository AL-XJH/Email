import { uiText, useLanguage as useUiLanguage } from "@/lib/language"
import { Button } from "@/components/ui/button"

export function AuthLoading() {
  useUiLanguage()

  return (
    <div className="flex h-screen bg-background">
      <div className="w-[232px] shrink-0 border-r border-border p-4">
        <div className="flex items-center gap-2">
          <div className="h-8 w-8 animate-pulse rounded-full bg-muted" />
          <div className="h-4 w-24 animate-pulse rounded bg-muted" />
        </div>
        <div className="mt-3 h-9 animate-pulse rounded-md bg-muted" />
        <div className="mt-2 h-9 animate-pulse rounded-md bg-muted" />
        <div className="mt-4 space-y-1.5">
          {Array.from({ length: 9 }).map((_, index) => (
            <div key={index} className="h-8 animate-pulse rounded-md bg-muted/60" />
          ))}
        </div>
      </div>
      <div className="flex-1" />
    </div>
  )
}

export function AuthError({ message, onRetry }: { message: string; onRetry: () => void }) {
  useUiLanguage()

  return (
    <div className="grid min-h-screen place-items-center bg-background px-4">
      <div className="w-full max-w-sm space-y-4 text-center">
        <div className="text-sm font-medium">{uiText("无法连接后端服务")}</div>
        <div className="text-sm text-muted-foreground">{uiText(message)}</div>
        <Button type="button" variant="outline" onClick={onRetry}>
          {uiText("重新加载")}
        </Button>
      </div>
    </div>
  )
}
