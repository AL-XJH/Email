import * as React from "react"
import { Check, Copy, Mail, MailCheck, Paperclip, Star } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useToast } from "@/hooks/use-toast"
import type { MailLabel, MailMessage } from "@/lib/api-types"
import { uiText, useLanguage as useUiLanguage } from "@/lib/language"
import { copyToClipboard } from "@/lib/clipboard"
import { labelDotColor, senderDisplayName, senderTitle } from "@/lib/mail-format"
import { cn, formatDate, formatListDate } from "@/lib/utils"
import { extractVerificationCode } from "@/lib/verification-code"

type MessageRowProps = {
  message: MailMessage
  active: boolean
  checked: boolean
  scheduled?: boolean
  onCheckedChange: (checked: boolean) => void
  onClick: () => void
  onContextMenu: (event: React.MouseEvent) => void
  onStar: () => void
  canOrganize: boolean
}

// 行组件只在「这封信本身」或选中态变化时重渲染：父级每次渲染都会新建
// onClick/onStar 闭包，默认的浅比较会让 memo 完全失效。
function messageRowPropsEqual(prev: MessageRowProps, next: MessageRowProps) {
  return (
    prev.message === next.message &&
    prev.active === next.active &&
    prev.checked === next.checked &&
    Boolean(prev.scheduled) === Boolean(next.scheduled) &&
    prev.canOrganize === next.canOrganize
  )
}

export const CompactMessageRow = React.memo(function CompactMessageRow({
  message,
  active,
  checked,
  scheduled,
  onCheckedChange,
  onClick,
  onContextMenu,
  onStar,
  canOrganize,
}: {
  message: MailMessage
  active: boolean
  checked: boolean
  scheduled?: boolean
  onCheckedChange: (checked: boolean) => void
  onClick: () => void
  onContextMenu: (event: React.MouseEvent) => void
  onStar: () => void
  canOrganize: boolean
}) {
  useUiLanguage()

  const visibleLabels = (message.labels || []).slice(0, 2)
  const hiddenLabelCount = Math.max((message.labels?.length || 0) - visibleLabels.length, 0)
  const senderName = senderDisplayName(message)
  return (
    <div
      onClick={onClick}
      onContextMenu={onContextMenu}
      className={cn(
        "cursor-pointer border-b px-3 py-3 text-sm transition-colors hover:bg-accent/50 sm:grid sm:items-center sm:gap-2 sm:px-4 sm:py-2",
        canOrganize
          ? "sm:grid-cols-[32px_28px_minmax(140px,220px)_minmax(0,1fr)_104px_36px]"
          : "sm:grid-cols-[28px_minmax(140px,220px)_minmax(0,1fr)_104px]",
        active && "bg-accent",
        !message.isRead && "font-semibold"
      )}
    >
      <div className="flex gap-3 sm:contents">
        {canOrganize && (
          <Checkbox
            aria-label={uiText("选择邮件")}
            checked={checked}
            onCheckedChange={(value) => onCheckedChange(value === true)}
            onClick={(event) => event.stopPropagation()}
            className="mt-0.5 shrink-0 sm:mt-0"
          />
        )}
        {message.isRead ? (
          <MailCheck className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground/70 sm:mt-0" />
        ) : (
          <Mail className="mt-0.5 h-4 w-4 shrink-0 fill-yellow-200 text-yellow-500 sm:mt-0" />
        )}
        <div className="min-w-0 flex-1 sm:contents">
          <div className="flex min-w-0 items-center justify-between gap-2 sm:block">
            <div className="min-w-0 truncate" title={senderTitle(message)}>
              {senderName}
            </div>
            <div className="flex shrink-0 items-center gap-1 sm:hidden">
              <span className="text-xs text-muted-foreground">
                {formatDate(message.receivedAt)}
              </span>
              {canOrganize && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={message.isStarred ? uiText("取消星标") : uiText("添加星标")}
                  className="h-7 w-7 text-muted-foreground hover:text-yellow-500"
                  onClick={(event) => {
                    event.stopPropagation()
                    onStar()
                  }}
                >
                  <Star
                    className={cn(
                      "h-4 w-4",
                      message.isStarred && "fill-yellow-400 text-yellow-500"
                    )}
                  />
                </Button>
              )}
            </div>
          </div>
          <div className="mt-1 flex min-w-0 items-center gap-2 sm:mt-0">
            <span className="truncate font-medium">{message.subject}</span>
            {(message.threadCount || 0) > 1 && (
              <Badge
                variant="outline"
                className="h-5 shrink-0 rounded-lg px-1.5 text-[11px] font-normal text-muted-foreground"
                title={uiText("会话邮件数")}
              >
                {uiText("会话 {0} 封", [message.threadCount || 0])}
              </Badge>
            )}
            <span className="hidden min-w-0 truncate text-muted-foreground sm:block">
              {message.snippet}
            </span>
            {scheduled && (
              <Badge
                variant="secondary"
                className="h-5 shrink-0 rounded-lg px-1.5 text-[11px] font-normal"
              >
                {uiText("已定时")}
              </Badge>
            )}
            {visibleLabels.map((label) => (
              <MailLabelBadge key={label.id} label={label} />
            ))}
            {hiddenLabelCount > 0 && (
              <Badge
                variant="outline"
                className="h-5 shrink-0 rounded-lg px-1.5 text-[11px] font-normal text-muted-foreground"
              >
                +{uiText("{0}", [hiddenLabelCount])}
              </Badge>
            )}
            {message.hasAttachments && (
              <Paperclip className="h-3 w-3 shrink-0 text-muted-foreground" />
            )}
          </div>
          <div className="mt-1 line-clamp-2 text-xs text-muted-foreground sm:hidden">
            {message.snippet}
          </div>
        </div>
      </div>
      <div className="hidden shrink-0 text-right text-xs text-muted-foreground sm:block">
        {formatDate(message.receivedAt)}
      </div>
      {canOrganize && (
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={message.isStarred ? uiText("取消星标") : uiText("添加星标")}
          className="hidden h-7 w-7 text-muted-foreground hover:text-yellow-500 sm:inline-flex"
          onClick={(event) => {
            event.stopPropagation()
            onStar()
          }}
        >
          <Star className={cn("h-4 w-4", message.isStarred && "fill-yellow-400 text-yellow-500")} />
        </Button>
      )}
    </div>
  )
}
, messageRowPropsEqual)

export const MessageRow = React.memo(function MessageRow({
  message,
  active,
  checked,
  scheduled,
  onCheckedChange,
  onClick,
  onContextMenu,
  onStar,
  canOrganize,
}: {
  message: MailMessage
  active: boolean
  checked: boolean
  scheduled?: boolean
  onCheckedChange: (checked: boolean) => void
  onClick: () => void
  onContextMenu: (event: React.MouseEvent) => void
  onStar: () => void
  canOrganize: boolean
}) {
  useUiLanguage()
  const { toast } = useToast()

  const visibleLabels = (message.labels || []).slice(0, 2)
  const hiddenLabelCount = Math.max((message.labels?.length || 0) - visibleLabels.length, 0)
  const senderName = senderDisplayName(message)
  const verificationCode = extractVerificationCode(message.subject, message.snippet)
  const [codeCopied, setCodeCopied] = React.useState(false)
  const codeCopiedTimer = React.useRef<number | undefined>(undefined)
  React.useEffect(() => () => window.clearTimeout(codeCopiedTimer.current), [])
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${senderName} ${message.subject}`.trim()}
      onClick={onClick}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault()
          onClick()
        }
      }}
      onContextMenu={onContextMenu}
      data-active={active ? "true" : undefined}
      className={cn(
        "mail-list-row group relative w-full cursor-pointer border-b border-border/60 px-3 py-1.5 transition-colors hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
        active && "bg-accent"
      )}
    >
      <div className="relative z-10 flex items-start gap-3">
        {canOrganize && (
          <div className="hidden w-5 flex-shrink-0 items-center pt-1 md:flex">
            <Checkbox
              aria-label={uiText("选择邮件")}
              checked={checked}
              onCheckedChange={(value) => onCheckedChange(value === true)}
              onClick={(event) => event.stopPropagation()}
              className={cn(
                "h-4 w-4 transition-opacity duration-150",
                checked ? "opacity-100" : "opacity-0 group-hover:opacity-100"
              )}
            />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <div className="mb-0.5 flex items-center justify-between gap-2">
            <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
              <span
                className={cn(
                  "max-w-[140px] truncate text-sm font-semibold md:max-w-[180px]",
                  message.isRead ? "text-foreground/70" : "text-foreground"
                )}
                title={senderTitle(message)}
              >
                {senderName}
              </span>
              {message.hasAttachments && (
                <Paperclip className="h-3 w-3 shrink-0 text-muted-foreground" />
              )}
            </div>
            <div className="relative flex h-5 flex-shrink-0 items-center justify-end">
              <span
                className={cn(
                  "text-xs tabular-nums text-muted-foreground transition-opacity duration-150",
                  "md:group-hover:opacity-0"
                )}
              >
                {formatListDate(message.receivedAt)}
              </span>
              {canOrganize && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={message.isStarred ? uiText("取消星标") : uiText("添加星标")}
                  className={cn(
                    "absolute right-0 top-0 h-5 w-5 text-muted-foreground hover:text-yellow-500",
                    message.isStarred
                      ? "opacity-100"
                      : "opacity-0 group-hover:opacity-100 md:group-hover:opacity-100"
                  )}
                  onClick={(e) => {
                    e.stopPropagation()
                    onStar()
                  }}
                >
                  <Star
                    className={cn(
                      "h-3.5 w-3.5",
                      message.isStarred && "fill-yellow-400 text-yellow-500"
                    )}
                  />
                </Button>
              )}
            </div>
          </div>
          <div className="mb-0.5 flex items-center gap-1.5 truncate text-sm font-medium leading-[18px]">
            <span className="truncate">{message.subject}</span>
            {scheduled && (
              <Badge variant="secondary" className="h-4 shrink-0 rounded px-1 text-[10px] font-normal">
                {uiText("已定时")}
              </Badge>
            )}
          </div>
          <div className="flex items-center justify-between">
            <div className="mail-list-row-preview flex min-w-0 flex-1 items-center gap-2 text-[13px] leading-[18px] text-muted-foreground">
              {verificationCode && (
                <button
                  type="button"
                  data-copied={codeCopied ? "true" : undefined}
                  className={cn(
                    "verification-code-chip inline-flex shrink-0 cursor-pointer items-center gap-1.5 rounded-lg border px-1.5 py-0.5 text-xs",
                    "transition-[background-color,border-color,color,box-shadow,transform] duration-150 ease-out",
                    "active:scale-[0.94]",
                    codeCopied
                      ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 ring-1 ring-emerald-500/20 dark:text-emerald-400"
                      : "border-border bg-transparent text-foreground hover:border-foreground/25 hover:bg-accent"
                  )}
                  title={uiText("复制验证码")}
                  onClick={(event) => {
                    event.stopPropagation()
                    const finish = (ok: boolean) => {
                      if (!ok) {
                        toast({ title: uiText("复制失败"), variant: "destructive" })
                        return
                      }
                      setCodeCopied(true)
                      window.clearTimeout(codeCopiedTimer.current)
                      codeCopiedTimer.current = window.setTimeout(() => setCodeCopied(false), 1400)
                    }
                    void copyToClipboard(verificationCode).then(finish)
                  }}
                >
                  <span
                    className={cn(
                      "hidden font-medium sm:inline",
                      codeCopied ? "" : "text-foreground/80"
                    )}
                  >
                    {codeCopied ? uiText("已复制") : uiText("验证码")}
                  </span>
                  <code className="font-mono font-semibold">{verificationCode}</code>
                  {codeCopied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3 opacity-60" />}
                </button>
              )}
              {message.snippet && <span className="min-w-0 truncate">{message.snippet}</span>}
            </div>
            {(visibleLabels.length > 0 || hiddenLabelCount > 0) && (
              <div className="ml-2 flex flex-shrink-0 items-center gap-1">
                {visibleLabels.map((label) => (
                  <MailLabelBadge key={label.id} label={label} />
                ))}
                {hiddenLabelCount > 0 && (
                  <span className="text-[11px] text-muted-foreground">
                    +{uiText("{0}", [hiddenLabelCount])}
                  </span>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
, messageRowPropsEqual)

function MailLabelBadge({ label }: { label: MailLabel }) {
  useUiLanguage()

  return (
    <Badge variant="outline" className="shrink-0 gap-1.5 rounded-lg font-normal">
      <span
        className="h-2 w-2 shrink-0 rounded-full"
        style={{ backgroundColor: labelDotColor(label) }}
      />
      {label.name}
    </Badge>
  )
}
