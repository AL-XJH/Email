import * as React from "react"
import {
  ArrowLeft,
  Archive,
  Ellipsis,
  Forward,
  History,
  MailCheck,
  PencilLine,
  Plus,
  Reply,
  ReplyAll,
  Star,
  Trash2,
  X,
} from "lucide-react"

import { useMutation } from "@tanstack/react-query"

import { MailAttachment } from "@/components/mail-attachment"
import { MailHtmlFrame } from "@/components/mail-html-frame"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { SidebarMenuButton } from "@/components/ui/sidebar"
import { Skeleton } from "@/components/ui/skeleton"
import { api } from "@/lib/api"
import type { MailLabel, MailMessage } from "@/lib/api-types"
import { uiText, type Language, useLanguage as useUiLanguage } from "@/lib/language"
import { useToast } from "@/hooks/use-toast"
import { errorMessage } from "@/lib/ui-errors"
import {
  accountInitial,
  attachmentHref,
  extractAddress,
  labelDotColor,
  normalizeTranslationLanguage,
  senderAddress,
  senderDisplayName,
  senderTitle,
  stripHtml,
  translationTargetLabel,
} from "@/lib/mail-format"
import { shouldOfferTranslation } from "@/lib/translation-offer"
import { cn, formatBytes, formatDate, formatDateTime } from "@/lib/utils"

export function CompactMessageDetail({
  selected,
  threadMessages,
  loading,
  labels,
  labelPending,
  previousMessage,
  nextMessage,
  onBack,
  onSelect,
  onStar,
  onReply,
  onReplyAll,
  onForward,
  onSendTimeline,
  onArchive,
  onDelete,
  onToggleRead,
  onAddLabel,
  onRemoveLabel,
  canSend,
  canOrganize,
  canManageLabels,
  canDownloadAttachments,
  language,
}: {
  selected?: MailMessage
  threadMessages: MailMessage[]
  loading: boolean
  labels: MailLabel[]
  labelPending: boolean
  previousMessage?: MailMessage
  nextMessage?: MailMessage
  onBack: () => void
  onSelect: (id: string | null) => void
  onStar: (message: MailMessage) => void
  onReply: (message: MailMessage) => void
  onReplyAll: (message: MailMessage) => void
  onForward: (message: MailMessage) => void
  onSendTimeline: (message: MailMessage) => void
  onArchive: (message: MailMessage) => void
  onDelete: (message: MailMessage) => void
  onToggleRead: (message: MailMessage) => void
  onAddLabel: (message: MailMessage, label: MailLabel) => void
  onRemoveLabel: (message: MailMessage, labelId: string) => void
  canSend: boolean
  canOrganize: boolean
  canManageLabels: boolean
  canDownloadAttachments: boolean
  language: Language
}) {
  useUiLanguage()

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div className="shrink-0 border-b px-3 py-2 sm:px-4">
        <div className="flex min-h-10 items-center gap-2 sm:hidden">
          <Button variant="ghost" size="icon" onClick={onBack} aria-label={uiText("返回")}>
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="min-w-0 flex-1 truncate text-sm font-semibold">
            {selected?.subject || uiText("邮件详情")}
          </div>
          <Button
            variant="ghost"
            size="icon"
            disabled={!previousMessage}
            onClick={() => previousMessage && onSelect(previousMessage.id)}
            aria-label={uiText("上一封")}
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            disabled={!nextMessage}
            onClick={() => nextMessage && onSelect(nextMessage.id)}
            aria-label={uiText("下一封")}
          >
            <ArrowLeft className="h-4 w-4 rotate-180" />
          </Button>
          {selected && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={uiText("更多操作")}>
                  <Ellipsis className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {selected.folder === "Drafts" ? (
                  <DropdownMenuItem onSelect={() => onSelect(selected.id)}>
                    <PencilLine className="h-4 w-4" />
                    {uiText("编辑草稿")}
                  </DropdownMenuItem>
                ) : (
                  <>
                    {canSend && (
                      <DropdownMenuItem onSelect={() => onReply(selected)}>
                        <Reply className="h-4 w-4" />
                        {uiText("回复")}
                      </DropdownMenuItem>
                    )}
                    {canSend && (
                      <DropdownMenuItem onSelect={() => onReplyAll(selected)}>
                        <ReplyAll className="h-4 w-4" />
                        {uiText("全部回复")}
                      </DropdownMenuItem>
                    )}
                    {canSend && (
                      <DropdownMenuItem onSelect={() => onForward(selected)}>
                        <Forward className="h-4 w-4" />
                        {uiText("转发")}
                      </DropdownMenuItem>
                    )}
                    {selected.sendQueueId && (
                      <DropdownMenuItem onSelect={() => onSendTimeline(selected)}>
                        <History className="h-4 w-4" />
                        {uiText("投递时间线")}
                      </DropdownMenuItem>
                    )}
                    {canOrganize && (
                      <DropdownMenuItem onSelect={() => onArchive(selected)}>
                        <Archive className="h-4 w-4" />
                        {selected.folder === "Archive" ? uiText("取消归档") : uiText("归档")}
                      </DropdownMenuItem>
                    )}
                    {canOrganize && (
                      <DropdownMenuItem onSelect={() => onToggleRead(selected)}>
                        <MailCheck className="h-4 w-4" />
                        {selected.isRead ? uiText("标为未读") : uiText("标为已读")}
                      </DropdownMenuItem>
                    )}
                    {canOrganize && (
                      <DropdownMenuItem onSelect={() => onStar(selected)}>
                        <Star
                          className={cn(
                            "h-4 w-4",
                            selected.isStarred && "fill-yellow-400 text-yellow-500"
                          )}
                        />
                        {selected.isStarred ? uiText("取消星标") : uiText("添加星标")}
                      </DropdownMenuItem>
                    )}
                  </>
                )}
                {canOrganize && (
                  <DropdownMenuItem
                    onSelect={() => onDelete(selected)}
                    className="text-destructive"
                  >
                    <Trash2 className="h-4 w-4" />
                    {uiText("删除")}
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
        <div className="hidden min-h-10 items-center justify-between gap-3 sm:flex">
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={onBack}>
              <ArrowLeft className="h-4 w-4" />
              {uiText("返回")}
            </Button>
            {selected?.folder === "Drafts" ? (
              <Button variant="outline" size="sm" onClick={() => onSelect(selected.id)}>
                <PencilLine className="h-4 w-4" />
                {uiText("编辑草稿")}
              </Button>
            ) : (
              <>
                {selected && canSend && (
                  <Button variant="outline" size="sm" onClick={() => onReply(selected)}>
                    <Reply className="h-4 w-4" />
                    {uiText("回复")}
                  </Button>
                )}
                {selected && canSend && (
                  <Button variant="outline" size="sm" onClick={() => onReplyAll(selected)}>
                    <ReplyAll className="h-4 w-4" />
                    {uiText("全部回复")}
                  </Button>
                )}
                {selected && canSend && (
                  <Button variant="outline" size="sm" onClick={() => onForward(selected)}>
                    <Forward className="h-4 w-4" />
                    {uiText("转发")}
                  </Button>
                )}
                {selected?.sendQueueId && (
                  <Button variant="outline" size="sm" onClick={() => onSendTimeline(selected)}>
                    <History className="h-4 w-4" />
                    {uiText("投递时间线")}
                  </Button>
                )}
                {selected && canOrganize && (
                  <Button variant="outline" size="sm" onClick={() => onArchive(selected)}>
                    {selected.folder === "Archive" ? uiText("取消归档") : uiText("归档")}
                  </Button>
                )}
                {selected && canOrganize && (
                  <Button variant="outline" size="sm" onClick={() => onToggleRead(selected)}>
                    <MailCheck className="h-4 w-4" />
                    {selected.isRead ? uiText("标为未读") : uiText("标为已读")}
                  </Button>
                )}
                {selected && canOrganize && (
                  <Button variant="outline" size="sm" onClick={() => onStar(selected)}>
                    <Star
                      className={cn(
                        "h-4 w-4",
                        selected.isStarred && "fill-yellow-400 text-yellow-500"
                      )}
                    />
                    {selected.isStarred ? uiText("取消星标") : uiText("添加星标")}
                  </Button>
                )}
              </>
            )}
            {selected && canOrganize && (
              <Button variant="outline" size="sm" onClick={() => onDelete(selected)}>
                <Trash2 className="h-4 w-4" />
                {uiText("删除")}
              </Button>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={!previousMessage}
              onClick={() => previousMessage && onSelect(previousMessage.id)}
            >
              {uiText("上一封")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={!nextMessage}
              onClick={() => nextMessage && onSelect(nextMessage.id)}
            >
              {uiText("下一封")}
            </Button>
          </div>
        </div>
      </div>
      {loading && (
        <div className="space-y-4 p-8">
          <Skeleton className="h-8 w-2/3" />
          <Skeleton className="h-4 w-1/3" />
          <Separator />
          <Skeleton className="h-64 w-full" />
        </div>
      )}
      {!loading && !selected && (
        <div className="grid flex-1 place-items-center text-sm text-muted-foreground">
          {uiText("邮件不存在")}
        </div>
      )}
      {selected && (
        <ScrollArea className="min-h-0 flex-1">
          <div className="w-full px-4 py-4 sm:px-8 sm:py-6">
            <div className="space-y-5 border-b pb-5">
              <div className="flex items-start gap-3">
                <h1 className="min-w-0 flex-1 break-words text-xl font-semibold tracking-tight sm:text-2xl">
                  {selected.subject}
                </h1>
                {canOrganize && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={selected.isStarred ? uiText("取消星标") : uiText("添加星标")}
                    className="text-muted-foreground hover:text-yellow-500"
                    onClick={() => onStar(selected)}
                  >
                    <Star
                      className={cn(
                        "h-5 w-5",
                        selected.isStarred && "fill-yellow-400 text-yellow-500"
                      )}
                    />
                  </Button>
                )}
              </div>
              <MessageMetaPanel
                message={selected}
                {...(canManageLabels
                  ? {
                      availableLabels: labels,
                      onAddLabel: (label: MailLabel) => onAddLabel(selected, label),
                      onRemoveLabel: (labelId: string) => onRemoveLabel(selected, labelId),
                      labelPending,
                    }
                  : {})}
              />
            </div>
            <div className="py-6 sm:py-8">
              {threadMessages.length > 1 && (
                <div className="mb-5 rounded-lg border bg-muted/20 p-3">
                  <div className="mb-2 text-sm font-medium">
                    {uiText("同会话邮件 ({0})", [threadMessages.length])}
                  </div>
                  <div className="space-y-1">
                    {threadMessages
                      .filter((item) => item.id !== selected.id)
                      .map((item) => (
                        <Button
                          type="button"
                          variant="ghost"
                          key={item.id}
                          onClick={() => onSelect(item.id)}
                          className="h-auto w-full justify-start truncate px-2 py-1 text-left text-sm font-normal"
                        >
                          {item.subject || uiText("(无主题)")}{" "}
                          <span className="ml-1 text-muted-foreground">
                            {formatDate(item.receivedAt)}
                          </span>
                        </Button>
                      ))}
                  </div>
                </div>
              )}
              <TranslatableMailBody key={selected.id} message={selected} language={language} />
              {selected.attachments && selected.attachments.length > 0 && (
                <div className="mt-8 rounded-lg border p-4">
                  <div className="mb-3 font-medium">{uiText("附件")}</div>
                  <div className="space-y-2">
                    {selected.attachments.map((a) => (
                      <MailAttachment
                        key={`${selected.id}-${a.id}`}
                        {...a}
                        href={attachmentHref(selected, a.id)}
                        sizeLabel={formatBytes(a.sizeBytes)}
                        allowed={canDownloadAttachments}
                      />
                    ))}
                  </div>
                </div>
              )}
            </div>
          </div>
        </ScrollArea>
      )}
    </div>
  )
}
export function TranslatableMailBody({
  message,
  language,
}: {
  message: MailMessage
  language: Language
}) {
  useUiLanguage()

  const [translatedText, setTranslatedText] = React.useState("")
  const [translatedHtml, setTranslatedHtml] = React.useState("")
  const [showTranslated, setShowTranslated] = React.useState(false)
  const [truncated, setTruncated] = React.useState(false)
  const { toast } = useToast()
  const targetLanguage = normalizeTranslationLanguage(language)
  // 判断用「界面上真正显示的那份正文」：有 HTML 就以 HTML 为准，
  // 纯文本部分可能塞满追踪链接（见 lib/translation-offer.ts 的说明）。
  const sourceText = React.useMemo(
    () => (stripHtml(message.bodyHtml || "") || message.bodyText || message.snippet || "").trim(),
    [message.bodyHtml, message.bodyText, message.snippet]
  )
  const shouldShow = targetLanguage && shouldOfferTranslation(sourceText, language)
  const translatedMessage = React.useMemo<MailMessage>(
    () => ({ ...message, bodyText: translatedText, bodyHtml: translatedHtml }),
    [message, translatedHtml, translatedText]
  )
  const {
    mutate: translate,
    reset: resetTranslation,
    isPending: translationPending,
  } = useMutation({
    mutationFn: () =>
      message.externalAccountId
        ? api.translateExternalMessage(message.externalAccountId, message.id, targetLanguage!)
        : api.translateMessage(message.id, targetLanguage!),
    onSuccess: (result) => {
      setTranslatedText(result.translatedText)
      setTranslatedHtml(result.translatedHtml || "")
      setTruncated(result.truncated)
      setShowTranslated(true)
    },
    onError: (error) =>
      toast({
        title: "翻译失败",
        description: errorMessage(error),
      }),
  })

  React.useEffect(() => {
    setTranslatedText("")
    setTranslatedHtml("")
    setShowTranslated(false)
    setTruncated(false)
    resetTranslation()
  }, [language, message.id, resetTranslation])

  return (
    <>
      {(shouldShow || translatedText) && (
        <div className="mb-4 rounded-lg border bg-muted/30 p-3 text-sm">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-muted-foreground">
              {translatedText
                ? uiText("已翻译为 {0}，当前{1}", [
                    uiText(translationTargetLabel(language)),
                    showTranslated ? "显示译文" : "显示原文",
                  ])
                : uiText("检测到邮件可能不是当前语言，可翻译为 {0}", [
                    translationTargetLabel(language),
                  ])}
              {truncated && <span className="ml-1">{uiText("（内容较长，仅翻译前半部分）")}</span>}
            </div>
            <div className="flex items-center gap-2">
              {translatedText && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setShowTranslated((value) => !value)}
                >
                  {showTranslated ? uiText("显示原文") : uiText("显示译文")}
                </Button>
              )}
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={translationPending}
                onClick={() => translate()}
              >
                {translationPending
                  ? uiText("翻译中...")
                  : translatedText
                    ? uiText("重新翻译")
                    : uiText("翻译")}
              </Button>
            </div>
          </div>
        </div>
      )}
      {translatedText && showTranslated ? (
        <MailHtmlFrame
          bodyHtml={translatedMessage.bodyHtml}
          bodyText={translatedMessage.bodyText}
        />
      ) : (
        <MailHtmlFrame bodyHtml={message.bodyHtml} bodyText={message.bodyText} />
      )}
    </>
  )
}
export function NewLabelButton({
  collapsed,
  pending,
  onCreate,
  editing,
  onEditingChange,
}: {
  collapsed: boolean
  pending: boolean
  onCreate: (name: string) => void
  editing?: boolean
  onEditingChange?: (v: boolean) => void
}) {
  useUiLanguage()

  const [internalEditing, setInternalEditing] = React.useState(false)
  const isEditing = editing ?? internalEditing
  const setEditingState = onEditingChange ?? setInternalEditing
  const [value, setValue] = React.useState("")
  if (collapsed) {
    return (
      <SidebarMenuButton className="justify-center px-0" onClick={() => setEditingState(true)}>
        <Plus className="h-4 w-4" />
      </SidebarMenuButton>
    )
  }
  if (isEditing) {
    return (
      <form
        className="px-2 py-1"
        onSubmit={(event) => {
          event.preventDefault()
          const name = value.trim()
          if (!name) return
          onCreate(name)
          setValue("")
          setEditingState(false)
        }}
      >
        <Input
          autoFocus
          value={value}
          onChange={(event) => setValue(event.target.value)}
          onBlur={() => {
            if (!value.trim()) setEditingState(false)
          }}
          placeholder={uiText("新建标签")}
          disabled={pending}
        />
      </form>
    )
  }
  return (
    <SidebarMenuButton className="text-muted-foreground" onClick={() => setEditingState(true)}>
      <Plus className="h-4 w-4" />
      <span>{uiText("新建标签")}</span>
    </SidebarMenuButton>
  )
}

function cleanAddressList(items?: string[]) {
  return (items || []).map((item) => extractAddress(item)).filter(Boolean)
}

function sameMailTime(a?: string, b?: string) {
  if (!a || !b) return false
  const left = new Date(a).getTime()
  const right = new Date(b).getTime()
  return !Number.isNaN(left) && !Number.isNaN(right) && left === right
}

export function MessageMetaPanel({
  message,
  availableLabels,
  onAddLabel,
  onRemoveLabel,
  labelPending,
}: {
  message: MailMessage
  availableLabels?: MailLabel[]
  onAddLabel?: (label: MailLabel) => void
  onRemoveLabel?: (labelId: string) => void
  labelPending?: boolean
}) {
  useUiLanguage()

  const fromName = senderDisplayName(message)
  const fromAddress = senderAddress(message) || uiText("未知发件人地址")
  const to = cleanAddressList(message.to)
  const cc = cleanAddressList(message.cc)
  const bcc = cleanAddressList(message.bcc)
  const deliveredTo = extractAddress(message.recipientAddress || message.mailboxAddress || "")
  const showSentAt = Boolean(message.sentAt) && !sameMailTime(message.sentAt, message.receivedAt)
  const labels = message.labels || []

  return (
    <div className="space-y-3 rounded-xl border bg-muted/20 p-3 text-sm">
      <div className="flex min-w-0 items-start gap-3">
        <Avatar className="size-10 shrink-0 rounded-full">
          <AvatarFallback className="bg-primary text-sm font-semibold text-primary-foreground">
            {accountInitial(fromName, fromAddress)}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0 flex-1 space-y-2">
          <MessageMetaRow label={uiText("发件人")}>
            <span className="break-words font-medium text-foreground" title={senderTitle(message)}>
              {fromName}
            </span>
          </MessageMetaRow>
          <MessageMetaRow label={uiText("发件人地址")}>
            <span className="break-all">{fromAddress}</span>
          </MessageMetaRow>
          <MessageMetaRow label={uiText("收件人")}>
            <AddressList values={to} empty={uiText("未填写收件人")} />
          </MessageMetaRow>
          {cc.length > 0 && (
            <MessageMetaRow label={uiText("抄送")}>
              <AddressList values={cc} />
            </MessageMetaRow>
          )}
          {bcc.length > 0 && (
            <MessageMetaRow label={uiText("密送")}>
              <AddressList values={bcc} />
            </MessageMetaRow>
          )}
          {deliveredTo && (
            <MessageMetaRow label={uiText("投递邮箱")}>
              <span className="break-all">{deliveredTo}</span>
            </MessageMetaRow>
          )}
          {showSentAt && (
            <MessageMetaRow label={uiText("发送时间")}>
              <span>{formatDateTime(message.sentAt)}</span>
            </MessageMetaRow>
          )}
          <MessageMetaRow label={uiText("接收时间")}>
            <span>{formatDateTime(message.receivedAt)}</span>
          </MessageMetaRow>
          {availableLabels && onAddLabel && onRemoveLabel && (
            <MessageMetaRow label={uiText("标签")}>
              <div className="flex flex-wrap items-center gap-1.5">
                {labels.map((label) => {
                  const colors = { backgroundColor: labelDotColor(label) }
                  return (
                    <Badge
                      key={label.id}
                      variant="outline"
                      className="label-badge group/badge gap-1.5 rounded-lg font-normal"
                    >
                      <span
                        className="h-2 w-2 shrink-0 rounded-full"
                        style={{ backgroundColor: colors.backgroundColor }}
                      />
                      <span>{label.name}</span>
                      <button
                        type="button"
                        className="label-badge-delete -mr-0.5 flex h-3.5 w-3.5 items-center justify-center rounded-full opacity-0 transition-opacity hover:bg-muted group-hover/badge:opacity-100"
                        onClick={() => onRemoveLabel(label.id)}
                        disabled={labelPending}
                        aria-label={uiText("移除标签 {0}", [label.name])}
                      >
                        <X className="h-2.5 w-2.5" />
                      </button>
                    </Badge>
                  )
                })}
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <button
                      type="button"
                      className="inline-flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-lg border text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                      aria-label={uiText("添加标签")}
                    >
                      <Plus className="h-3 w-3" />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="start" className="w-48">
                    {availableLabels.length === 0 && (
                      <DropdownMenuItem disabled>{uiText("请先在侧栏新建标签")}</DropdownMenuItem>
                    )}
                    {availableLabels.map((label) => {
                      const active = labels.some((l) => l.id === label.id)
                      const colors = { backgroundColor: labelDotColor(label) }
                      return (
                        <DropdownMenuCheckboxItem
                          key={label.id}
                          checked={active}
                          onSelect={(event) => {
                            event.preventDefault()
                            if (active) onRemoveLabel(label.id)
                            else onAddLabel(label)
                          }}
                        >
                          <span
                            className="mr-2 h-2 w-2 rounded-full"
                            style={{ backgroundColor: colors.backgroundColor }}
                          />
                          <span>{label.name}</span>
                        </DropdownMenuCheckboxItem>
                      )
                    })}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            </MessageMetaRow>
          )}
        </div>
      </div>
    </div>
  )
}

function MessageMetaRow({ label, children }: { label: string; children: React.ReactNode }) {
  useUiLanguage()

  return (
    <div className="grid gap-1 sm:grid-cols-[5rem_minmax(0,1fr)]">
      <div className="shrink-0 text-xs font-medium text-muted-foreground sm:pt-0.5">
        {uiText(label)}
      </div>
      <div className="min-w-0 text-foreground">{children}</div>
    </div>
  )
}

function AddressList({ values, empty = "无" }: { values: string[]; empty?: string }) {
  useUiLanguage()

  if (values.length === 0) return <span className="text-muted-foreground">{empty}</span>
  return (
    <div className="flex min-w-0 flex-wrap gap-1.5">
      {values.map((value, index) => (
        <span
          key={`${value}-${index}`}
          className="inline-flex max-w-full rounded-lg bg-background px-2 py-0.5 text-xs text-foreground ring-1 ring-border"
        >
          <span className="truncate" title={value}>
            {value}
          </span>
        </span>
      ))}
    </div>
  )
}
