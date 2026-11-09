import {
  type UiText,
  uiMessage,
  getInitialLanguage,
  uiText,
  useLanguage as useUiLanguage,
} from "@/lib/language"
import { errorMessage } from "@/lib/ui-errors"
import * as React from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Node, mergeAttributes } from "@tiptap/core"
import { DOMParser as ProseMirrorDOMParser } from "@tiptap/pm/model"
import { EditorContent, useEditor } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import LinkExtension from "@tiptap/extension-link"
import ImageExtension from "@tiptap/extension-image"
import TextAlign from "@tiptap/extension-text-align"
import Placeholder from "@tiptap/extension-placeholder"
import {
  BackgroundColor,
  Color,
  FontFamily,
  FontSize,
  TextStyle,
} from "@tiptap/extension-text-style"
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  Bold,
  Calendar,
  Check,
  ChevronDown,
  Code2,
  Eraser,
  Eye,
  Highlighter,
  Image,
  IndentDecrease,
  IndentIncrease,
  Italic,
  Link,
  List,
  ListOrdered,
  Paperclip,
  Plus,
  Quote,
  Redo2,
  Send,
  Signature,
  Smile,
  Strikethrough,
  Type,
  Underline,
  Undo2,
  X,
} from "lucide-react"
import {
  api,
  Mailbox,
  MailMessage,
  SendPayload,
  DraftPayload,
  type PermissionLimits,
} from "@/lib/api"
import { cn, formatBytes, formatDate, formatDateTime } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { ConfirmDialog } from "@/components/confirm-dialog"
import {
  type ComposerValue,
  escapeHtml,
  htmlComposerValue,
  htmlContainsMeaningfulContent,
  plainTextComposerValue,
  plainTextToHtml,
  plainTextToHtmlFragment,
  sanitizeComposerHtml,
} from "@/components/mail-content"
import {
  type ScheduleDraft,
  defaultScheduleStartValue,
  durationLabel,
  formatTimeOnly,
  normalizeSchedule,
  parseScheduleStart,
  reminderLabel,
  repeatLabel,
  scheduleToFile,
} from "@/components/mail-schedule-utils"
import { MailHtmlFrame } from "@/components/mail-html-frame"
import { useIsMobile } from "@/hooks/use-mobile"
import { stripHtml } from "@/lib/mail-format"
import {
  composerFontOptions,
  composerFontSizeOptions,
  composerFontSizeValueByKey,
  composerEmojiOptions,
  composerMenuItemClass,
  composerHighlightColors,
  composerInitialHtml,
  composerTextColors,
  composerValueFromEditor,
  editorTextSelection,
  fontLabel,
  fontSizeLabel,
  normalizeFontName,
  normalizeFontSize,
  normalizeInsertUrl,
  selectedImageAttributes,
  splitQuotedComposerHtml,
} from "@/lib/composer-helpers"
import {
  type InsertDialogState,
  type InsertDialogValue,
  ComposeField,
  InsertContentDialog,
  ScheduleSendDialog,
  ToolbarTextButton,
} from "@/components/mail-composer-parts"
import { useToast } from "@/hooks/use-toast"
import type { ComposeDraft, ComposeSendIntent } from "@/lib/compose-types"

export function ComposeDialog({
  mailbox,
  open,
  draft,
  limits,
  canSend,
  canManageDrafts,
  canSchedule,
  canManageSignatures,
  onOpenChange,
  onSent,
}: {
  mailbox?: Mailbox
  open: boolean
  draft?: ComposeDraft
  limits?: PermissionLimits
  canSend: boolean
  canManageDrafts: boolean
  canSchedule: boolean
  canManageSignatures: boolean
  onOpenChange: (v: boolean) => void
  onSent: () => void
}) {
  useUiLanguage()

  const { toast } = useToast()
  const qc = useQueryClient()
  const [files, setFiles] = React.useState<File[]>([])
  const [draftAttachments, setDraftAttachments] = React.useState<SendPayload["attachments"]>([])
  const [attachmentsTouched, setAttachmentsTouched] = React.useState(false)
  const [draftId, setDraftId] = React.useState(draft?.id || "")
  const [toValue, setToValue] = React.useState(draft?.to || "")
  const [ccValue, setCcValue] = React.useState(draft?.cc || "")
  const [bccValue, setBccValue] = React.useState(draft?.bcc || "")
  const [subjectValue, setSubjectValue] = React.useState(draft?.subject || "")
  const [draftStatus, setDraftStatus] = React.useState<"idle" | "saving" | "saved" | "error">(
    "idle"
  )
  const [lastSavedAt, setLastSavedAt] = React.useState<Date | null>(null)
  const [scheduleDialogOpen, setScheduleDialogOpen] = React.useState(false)
  const [sendIntent, setSendIntent] = React.useState<ComposeSendIntent | null>(null)
  const sendStartedRef = React.useRef(false)
  const preparingRef = React.useRef(false)
  const sentRecipientsRef = React.useRef<Set<string>>(new Set())
  const lastSavedPayloadRef = React.useRef("")
  const [showCc, setShowCc] = React.useState(Boolean(draft?.cc))
  const [showBcc, setShowBcc] = React.useState(Boolean(draft?.bcc))
  const [sendSeparately, setSendSeparately] = React.useState(false)
  const defaultSignature = useQuery({
    queryKey: ["signature", "default", mailbox?.id],
    queryFn: () => api.defaultSignature(mailbox?.id),
    enabled: open && !!mailbox?.id && canManageSignatures,
  })
  const signatureText = defaultSignature.data?.signature?.content || ""
  const composerText =
    draft?.html ||
    (draft?.text !== undefined ? draft.text : signatureText ? `\n\n-- \n${signatureText}` : "")
  const draftSplit = React.useMemo(() => splitQuotedComposerHtml(draft?.html || ""), [draft?.html])
  const [quotedHtml, setQuotedHtml] = React.useState(draftSplit.quotedHtml)
  const [quotedText, setQuotedText] = React.useState(draftSplit.quotedText)
  const [body, setBody] = React.useState<ComposerValue>(() =>
    draft?.html
      ? htmlComposerValue(draftSplit.html)
      : plainTextComposerValue(composerText)
  )
  const activeMailboxId = draft?.mailboxId || mailbox?.id || ""
  const maxAttachmentBytes = attachmentLimitBytes(limits)
  const maxAttachmentText = maxAttachmentBytes > 0 ? formatBytes(maxAttachmentBytes) : "不限"
  const composePayload = React.useMemo<DraftPayload>(
    () => ({
      mailboxId: activeMailboxId,
      to: splitEmails(toValue),
      cc: showCc ? splitEmails(ccValue) : [],
      bcc: showBcc ? splitEmails(bccValue) : [],
      subject: subjectValue,
      text: quotedText ? `${body.text}\n\n${quotedText}` : body.text,
      html: [body.html || plainTextToHtml(body.text), quotedHtml].filter(Boolean).join(""),
      ...(attachmentsTouched ? { attachments: draftAttachments } : {}),
    }),
    [
      activeMailboxId,
      toValue,
      showCc,
      ccValue,
      showBcc,
      bccValue,
      subjectValue,
      body,
      quotedHtml,
      quotedText,
      attachmentsTouched,
      draftAttachments,
    ]
  )
  const hasDraftContent =
    open &&
    !!activeMailboxId &&
    (toValue.trim() ||
      ccValue.trim() ||
      bccValue.trim() ||
      subjectValue.trim() ||
      body.text.trim() ||
      body.html.trim())
  const send = useMutation({
    mutationFn: async (payloads: SendPayload[]) => {
      const sent: MailMessage[] = []
      for (const payload of payloads) {
        // 「分别发送」逐个发，第 3 个失败时前 2 个已经发出。重试时跳过
        // 已成功的收件人，否则他们会收到第二封。
        const key = payload.to.join(",")
        if (sentRecipientsRef.current.has(key)) continue
        sent.push(await api.send(payload))
        sentRecipientsRef.current.add(key)
      }
      return sent
    },
    onSuccess: async (_, payloads) => {
      if (draftId) {
        try {
          await api.deleteDraft(draftId)
        } catch {
          // The draft may already have been removed by another client.
        }
      }
      toast({
        title:
          payloads.length > 1 ? uiMessage("已分别发送 {0} 封邮件", [payloads.length]) : "发送成功",
      })
      sentRecipientsRef.current.clear()
      setFiles([])
      setDraftId("")
      onSent()
    },
    onError: (e) => {
      // 发信失败后必须解掉这个闸门，否则接下来的编辑再也不会自动存草稿。
      sendStartedRef.current = false
      toast({ title: "发送失败", description: errorMessage(e) })
    },
  })
  const scheduleSend = useMutation({
    mutationFn: (payload: SendPayload & { draftId?: string; sendAt: string }) =>
      api.scheduleSend(payload),
    onSuccess: (scheduled) => {
      sendStartedRef.current = true
      toast({ title: uiMessage("已定时发送 {0}", [{ date: scheduled.sendAt }]) })
      setScheduleDialogOpen(false)
      setFiles([])
      void Promise.all([
        qc.invalidateQueries({ queryKey: ["messages"] }),
        qc.invalidateQueries({ queryKey: ["folders"] }),
        qc.invalidateQueries({ queryKey: ["mail-stats"] }),
        qc.invalidateQueries({ queryKey: ["scheduled-sends"] }),
      ])
      onSent()
    },
    onError: (e) => {
      sendStartedRef.current = false
      toast({ title: "定时发送失败", description: errorMessage(e) })
    },
  })

  React.useEffect(() => {
    if (!open) return
    sendStartedRef.current = false
    const nextShowCc = Boolean(draft?.cc)
    const nextShowBcc = Boolean(draft?.bcc)
    const nextSplit = splitQuotedComposerHtml(draft?.html || "")
    // 这里只用草稿本身初始化。默认签名是异步查回来的，以前把它拼进 composerText 又放进
    // 依赖数组 —— 签名一到就重跑这个 effect，把用户已经敲好的收件人/主题/正文清空。
    const nextBody = draft?.html
      ? htmlComposerValue(nextSplit.html)
      : plainTextComposerValue(draft?.text ?? "")
    const nextQuotedHtml = nextSplit.quotedHtml
    const nextQuotedText = nextSplit.quotedText
    lastSavedPayloadRef.current = JSON.stringify({
      mailboxId: draft?.mailboxId || mailbox?.id || "",
      to: splitEmails(draft?.to || ""),
      cc: nextShowCc ? splitEmails(draft?.cc || "") : [],
      bcc: nextShowBcc ? splitEmails(draft?.bcc || "") : [],
      subject: draft?.subject || "",
      text: nextQuotedText ? `${nextBody.text}\n\n${nextQuotedText}` : nextBody.text,
      html: [nextBody.html || plainTextToHtml(nextBody.text), nextQuotedHtml]
        .filter(Boolean)
        .join(""),
      draftId: draft?.id || "",
    })
    setDraftId(draft?.id || "")
    setToValue(draft?.to || "")
    setCcValue(draft?.cc || "")
    setBccValue(draft?.bcc || "")
    setSubjectValue(draft?.subject || "")
    setQuotedHtml(nextQuotedHtml)
    setQuotedText(nextQuotedText)
    setBody(nextBody)
    setDraftStatus("idle")
    setLastSavedAt(null)
    setShowCc(nextShowCc)
    setShowBcc(nextShowBcc)
    setSendSeparately(false)
    setFiles(draft?.files || [])
    setDraftAttachments([])
    setAttachmentsTouched(false)
    // 只按草稿身份重置编辑器；正文/主题是用户输入，故意不放进依赖数组。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    open,
    draft?.key,
    draft?.id,
    draft?.mailboxId,
    draft?.to,
    draft?.cc,
    draft?.bcc,
    draft?.subject,
    draft?.html,
    draft?.files,
    mailbox?.id,
  ])

  React.useEffect(() => {
    let cancelled = false
    Promise.all(files.map(fileToAttachment)).then((attachments) => {
      if (!cancelled) setDraftAttachments(attachments)
    })
    return () => {
      cancelled = true
    }
  }, [files])

  React.useEffect(() => {
    if (!open || sendStartedRef.current || !hasDraftContent || !canManageDrafts) return
    const payloadKey = JSON.stringify({ ...composePayload, draftId })
    if (payloadKey === lastSavedPayloadRef.current) return
    const timer = window.setTimeout(async () => {
      try {
        setDraftStatus("saving")
        const saved = await api.saveDraft(composePayload, draftId || undefined)
        setDraftId(saved.id)
        lastSavedPayloadRef.current = JSON.stringify({ ...composePayload, draftId: saved.id })
        setLastSavedAt(new Date())
        setDraftStatus("saved")
        await Promise.all([
          qc.invalidateQueries({ queryKey: ["messages"] }),
          qc.invalidateQueries({ queryKey: ["folders"] }),
          qc.invalidateQueries({ queryKey: ["mail-stats"] }),
        ])
      } catch {
        setDraftStatus("error")
      }
    }, 5000)
    return () => window.clearTimeout(timer)
  }, [open, hasDraftContent, composePayload, draftId, qc, canManageDrafts])

  function buildSendWarnings(attachmentsCount: number) {
    const warnings: string[] = []
    const normalizedBody = `${body.text}\n${stripHtml(body.html)}`.toLowerCase()
    if (!subjectValue.trim()) warnings.push("这封邮件还没有主题。")
    if (!body.text.trim() && !htmlContainsMeaningfulContent(body.html))
      warnings.push("正文还是空的。")
    if (/(附件|附上|见附件|attached|attachment)/i.test(normalizedBody) && attachmentsCount === 0)
      warnings.push("正文提到了附件，但还没有添加附件。")
    return warnings
  }

  function confirmOrRun(
    intent: Omit<ComposeSendIntent, "description"> & {
      warnings: string[]
      defaultDescription?: UiText
    }
  ) {
    if (intent.warnings.length === 0) {
      intent.onConfirm()
      return
    }
    setSendIntent({
      title: intent.title,
      description: {
        key: "",
        lines: [
          ...(intent.defaultDescription ? [intent.defaultDescription] : []),
          ...intent.warnings,
        ],
      },
      confirmText: intent.confirmText,
      onConfirm: intent.onConfirm,
    })
  }

  function addFiles(nextFiles: File[]) {
    if (nextFiles.length === 0) return
    const allowed =
      maxAttachmentBytes > 0
        ? nextFiles.filter((file) => file.size <= maxAttachmentBytes)
        : nextFiles
    const blockedCount = nextFiles.length - allowed.length
    if (blockedCount > 0) {
      toast({
        title: "附件超过权限组上限",
        description: uiMessage("当前单个附件上限 {0}", [maxAttachmentText]),
      })
    }
    if (allowed.length > 0) {
      setAttachmentsTouched(true)
      setFiles((current) => [...current, ...allowed])
    }
  }

  function attachmentsWithinLimit() {
    if (maxAttachmentBytes <= 0) return true
    if (files.every((file) => file.size <= maxAttachmentBytes)) return true
    toast({
      title: "附件超过权限组上限",
      description: uiMessage("当前单个附件上限 {0}", [maxAttachmentText]),
    })
    return false
  }

  async function prepareSend() {
    if (!canSend) return
    if (!mailbox) return
    // 附件转 base64 是异步的，转换期间「发送」按钮还没进入 isPending。
    // 没有这个同步闸门，双击/回车会并发跑两次 prepareSend，最后发出两封一样的信。
    if (preparingRef.current) return
    if (!attachmentsWithinLimit()) return
    preparingRef.current = true
    try {
      await prepareSendPrepared()
    } finally {
      preparingRef.current = false
    }
  }

  async function prepareSendPrepared() {
    if (!mailbox) return
    const attachments = await Promise.all(files.map(fileToAttachment))
    const to = splitEmails(toValue)
    const cc = showCc ? splitEmails(ccValue) : []
    const bcc = showBcc ? splitEmails(bccValue) : []
    const text = quotedText ? `${body.text}\n\n${quotedText}` : body.text
    const html = [body.html || plainTextToHtml(body.text), quotedHtml].filter(Boolean).join("")
    const payload: SendPayload = {
      mailboxId: mailbox.id,
      to,
      cc,
      bcc,
      subject: subjectValue,
      text,
      html,
      attachments,
    }
    const separateRecipients = Array.from(new Set([...to, ...cc, ...bcc]))
    const payloads =
      sendSeparately && separateRecipients.length > 0
        ? separateRecipients.map((recipient): SendPayload => ({
            ...payload,
            to: [recipient],
            cc: [],
            bcc: [],
          }))
        : [payload]
    confirmOrRun({
      title: "确认发送这封邮件？",
      confirmText: sendSeparately && payloads.length > 1 ? "继续分别发送" : "继续发送",
      warnings: buildSendWarnings(attachments.length),
      onConfirm: () => {
        sendStartedRef.current = true
        setSendIntent(null)
        send.mutate(payloads)
      },
    })
  }

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (!mailbox) {
      toast({ title: "请选择发件邮箱" })
      return
    }
    await prepareSend()
  }
  async function scheduleAt(sendAt: string) {
    if (!canSchedule) return
    if (!mailbox) {
      toast({ title: "请选择发件邮箱" })
      return
    }
    if (preparingRef.current) return
    if (!attachmentsWithinLimit()) return
    preparingRef.current = true
    try {
      await scheduleAtPrepared(sendAt)
    } finally {
      preparingRef.current = false
    }
  }

  async function scheduleAtPrepared(sendAt: string) {
    if (!mailbox) return
    const attachments = await Promise.all(files.map(fileToAttachment))
    const payload: SendPayload & { draftId?: string; sendAt: string } = {
      mailboxId: mailbox.id,
      to: splitEmails(toValue),
      cc: showCc ? splitEmails(ccValue) : [],
      bcc: showBcc ? splitEmails(bccValue) : [],
      subject: subjectValue,
      text: quotedText ? `${body.text}\n\n${quotedText}` : body.text,
      html: [body.html || plainTextToHtml(body.text), quotedHtml].filter(Boolean).join(""),
      attachments,
      draftId: draftId || undefined,
      sendAt,
    }
    confirmOrRun({
      title: "确认定时发送？",
      confirmText: "继续定时发送",
      defaultDescription: uiMessage("发送时间：{0}", [{ date: sendAt }]),
      warnings: buildSendWarnings(attachments.length),
      onConfirm: () => {
        sendStartedRef.current = true
        setSendIntent(null)
        scheduleSend.mutate(payload)
      },
    })
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex h-dvh w-screen max-w-none overflow-hidden p-0 sm:h-auto sm:max-h-[92dvh] sm:w-[min(96vw,82rem)]"
        aria-describedby={undefined}
        onInteractOutside={(event) => event.preventDefault()}
        onPointerDownOutside={(event) => event.preventDefault()}
      >
        <form
          key={draft?.key || "new"}
          className="flex min-h-0 min-w-0 flex-1 flex-col sm:max-h-[90dvh]"
          onSubmit={submit}
        >
          <DialogHeader className="shrink-0 border-b px-4 py-3 text-left sm:px-6 sm:py-4">
            <DialogTitle className="flex min-w-0 flex-col gap-1 pr-8 sm:flex-row sm:items-center sm:justify-between sm:gap-4 sm:pr-6">
              <span>{draftId ? uiText("编辑草稿") : uiText("写信")}</span>
              <span
                className={cn(
                  "text-xs font-normal",
                  draftStatus === "error" ? "text-destructive" : "text-muted-foreground"
                )}
              >
                {draftStatus === "saving"
                  ? uiText("正在保存草稿...")
                  : draftStatus === "saved" && lastSavedAt
                    ? uiText("草稿已保存 {0}", [
                        lastSavedAt.toLocaleTimeString(getInitialLanguage(), {
                          hour: "2-digit",
                          minute: "2-digit",
                        }),
                      ])
                    : draftStatus === "error"
                      ? uiText("草稿保存失败")
                      : ""}
              </span>
            </DialogTitle>
          </DialogHeader>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
            <ComposeField label={uiText("发件邮箱")}>
              <Input
                value={mailbox?.address || uiText("未选择")}
                readOnly
                className="h-10 min-w-0 flex-none rounded-none border-0 px-0 shadow-none focus-visible:ring-0 sm:flex-1"
              />
            </ComposeField>
            <ComposeField
              label={uiText("收件人")}
              action={
                <div className="flex shrink-0 flex-wrap items-center justify-start gap-1 text-sm sm:justify-end sm:gap-2">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-8 px-2 font-normal"
                    onClick={() => setShowCc((value) => !value)}
                  >
                    {uiText("抄送")}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-8 px-2 font-normal"
                    onClick={() => setShowBcc((value) => !value)}
                  >
                    {uiText("密送")}
                  </Button>
                  <div className="flex items-center gap-2 rounded-lg px-2 py-1">
                    <Checkbox
                      id="compose-send-separately"
                      checked={sendSeparately}
                      onCheckedChange={(value) => setSendSeparately(value === true)}
                    />
                    <Label
                      htmlFor="compose-send-separately"
                      className="cursor-pointer text-sm font-normal"
                    >
                      {uiText("分别发送")}
                    </Label>
                  </div>
                </div>
              }
            >
              <Input
                name="to"
                placeholder={uiText("name@example.com，多个地址用逗号或空格分隔")}
                value={toValue}
                onChange={(event) => setToValue(event.target.value)}
                required
                className="h-10 min-w-0 flex-none rounded-none border-0 px-0 shadow-none focus-visible:ring-0 sm:flex-1"
              />
            </ComposeField>
            {showCc && (
              <ComposeField label={uiText("抄送")}>
                <Input
                  name="cc"
                  placeholder="cc@example.com"
                  value={ccValue}
                  onChange={(event) => setCcValue(event.target.value)}
                  className="h-10 min-w-0 flex-none rounded-none border-0 px-0 shadow-none focus-visible:ring-0 sm:flex-1"
                />
              </ComposeField>
            )}
            {showBcc && (
              <ComposeField label={uiText("密送")}>
                <Input
                  name="bcc"
                  placeholder="bcc@example.com"
                  value={bccValue}
                  onChange={(event) => setBccValue(event.target.value)}
                  className="h-10 min-w-0 flex-none rounded-none border-0 px-0 shadow-none focus-visible:ring-0 sm:flex-1"
                />
              </ComposeField>
            )}
            <ComposeField label={uiText("主　题")}>
              <Input
                name="subject"
                placeholder={uiText("输入主题")}
                value={subjectValue}
                onChange={(event) => setSubjectValue(event.target.value)}
                className="h-10 min-w-0 flex-none rounded-none border-0 px-0 shadow-none focus-visible:ring-0 sm:flex-1"
              />
            </ComposeField>
            <MailBodyComposer
              defaultValue={composerText}
              defaultHtml={draft?.html !== undefined ? draftSplit.html : undefined}
              quotedHtml={quotedHtml}
              quotedText={quotedText}
              onRemoveQuote={() => {
                setQuotedHtml("")
                setQuotedText("")
              }}
              files={files}
              signatureText={signatureText}
              maxAttachmentText={maxAttachmentText}
              onChange={setBody}
              onPickFiles={addFiles}
              onRemoveFile={(index) => {
                setAttachmentsTouched(true)
                setFiles((current) => current.filter((_, itemIndex) => itemIndex !== index))
              }}
            />
          </div>
          <DialogFooter className="flex shrink-0 flex-row flex-wrap justify-end gap-2 border-t bg-background px-4 py-3 sm:space-x-0 sm:px-6 sm:py-4">
            <Button
              type="button"
              variant="outline"
              className="min-h-10 px-3"
              onClick={() => onOpenChange(false)}
            >
              {uiText("取消")}
            </Button>
            {canSchedule && (
              <Button
                type="button"
                variant="outline"
                className="min-h-10 px-3"
                disabled={send.isPending || scheduleSend.isPending || !mailbox}
                onClick={() => setScheduleDialogOpen(true)}
              >
                <Calendar className="h-4 w-4" />
                {uiText("定时")}
              </Button>
            )}
            {canSend && (
              <Button className="min-h-10 px-4" disabled={send.isPending || !mailbox}>
                <Send className="h-4 w-4" />
                {send.isPending ? uiText("发送中...") : uiText("发送")}
              </Button>
            )}
          </DialogFooter>
        </form>
        <ScheduleSendDialog
          open={scheduleDialogOpen}
          pending={scheduleSend.isPending}
          onOpenChange={setScheduleDialogOpen}
          onConfirm={scheduleAt}
        />
        <ConfirmDialog
          open={!!sendIntent}
          title={sendIntent?.title || ""}
          description={sendIntent?.description}
          confirmText={sendIntent?.confirmText || uiText("继续")}
          pending={send.isPending || scheduleSend.isPending}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setSendIntent(null)
          }}
          onConfirm={() => sendIntent?.onConfirm()}
        />
      </DialogContent>
    </Dialog>
  )
}

const ScheduleCardNode = Node.create({
  name: "scheduleCard",
  group: "block",
  atom: true,
  selectable: true,
  draggable: false,
  addAttributes() {
    return {
      title: { default: "" },
      time: { default: "" },
      duration: { default: "" },
      reminder: { default: "" },
      repeat: { default: "" },
      location: { default: "" },
      description: { default: "" },
    }
  },
  parseHTML() {
    return [{ tag: "div[data-schedule-card]" }]
  },
  renderHTML({ HTMLAttributes }) {
    const { title, time, duration, reminder, repeat, location, description } = HTMLAttributes
    const rows = [
      ["时间", time],
      ["持续", duration],
      ["提醒", reminder],
      ["重复", repeat],
      location ? ["位置", location] : undefined,
      description ? ["描述", description] : undefined,
    ].filter(Boolean) as string[][]
    return [
      "div",
      mergeAttributes(HTMLAttributes, {
        "data-schedule-card": "true",
        style:
          "border:1px solid #d4d4d8;border-radius:8px;padding:14px 16px;margin:16px 0;background:#fafafa;",
      }),
      ["div", { style: "font-weight:600;font-size:16px;margin-bottom:10px;" }, title || "日程"],
      ...rows.map(([label, value]) => [
        "div",
        { style: "margin:6px 0;" },
        ["span", { style: "color:#71717a;" }, `${label}：`],
        value,
      ]),
    ]
  },
})

function scheduleToNodeAttributes(schedule: ScheduleDraft) {
  const start = parseScheduleStart(schedule)
  const end = schedule.allDay
    ? new Date(start.getTime() + 24 * 60 * 60 * 1000)
    : new Date(start.getTime() + schedule.durationMinutes * 60 * 1000)
  return {
    title: schedule.title,
    time: schedule.allDay
      ? formatDate(start.toISOString(), "zh-CN")
      : `${formatDateTime(start.toISOString(), "zh-CN")} - ${formatTimeOnly(end)}`,
    duration: schedule.allDay ? "全天" : durationLabel(schedule.durationMinutes),
    reminder: reminderLabel(schedule.reminderMinutes),
    repeat: repeatLabel(schedule.repeat),
    location: schedule.location,
    description: schedule.description,
  }
}

function MailBodyComposer({
  defaultValue,
  defaultHtml,
  quotedHtml,
  quotedText,
  onRemoveQuote,
  files,
  signatureText,
  maxAttachmentText,
  onChange,
  onPickFiles,
  onRemoveFile,
}: {
  defaultValue: string
  defaultHtml?: string
  quotedHtml?: string
  quotedText?: string
  onRemoveQuote?: () => void
  files: File[]
  signatureText: string
  maxAttachmentText: string
  onChange: (value: ComposerValue) => void
  onPickFiles: (files: File[]) => void
  onRemoveFile: (index: number) => void
}) {
  const [composerLanguage] = useUiLanguage()

  const fileInputRef = React.useRef<HTMLInputElement>(null)
  const dirtyRef = React.useRef(false)
  const lastDefaultRef = React.useRef(`${defaultValue}\n${defaultHtml || ""}`)
  const isMobile = useIsMobile()
  const [formatOpen, setFormatOpen] = React.useState(() =>
    typeof window === "undefined" ? true : window.innerWidth >= 768
  )
  const [scheduleOpen, setScheduleOpen] = React.useState(false)
  const [emojiOpen, setEmojiOpen] = React.useState(false)
  const [insertDialog, setInsertDialog] = React.useState<InsertDialogState | null>(null)
  const [previewOpen, setPreviewOpen] = React.useState(false)
  const [empty, setEmpty] = React.useState(!defaultValue.trim())
  const [selectionVersion, setSelectionVersion] = React.useState(0)

  React.useEffect(() => {
    setFormatOpen(!isMobile)
  }, [isMobile])

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ link: false }),
      TextStyle,
      Color,
      BackgroundColor,
      FontFamily,
      FontSize,
      LinkExtension.configure({
        openOnClick: false,
        enableClickSelection: true,
        HTMLAttributes: { target: "_blank", rel: "noopener noreferrer" },
      }),
      ImageExtension.configure({
        allowBase64: true,
        HTMLAttributes: { style: "max-width:100%;height:auto;border-radius:8px;margin:12px 0;" },
      }),
      TextAlign.configure({ types: ["heading", "paragraph"] }),
      Placeholder.configure({ placeholder: () => uiText("输入正文") }),
      ScheduleCardNode,
    ],
    content: composerInitialHtml(defaultValue, defaultHtml),
    editorProps: {
      attributes: {
        class:
          "mail-html min-h-[240px] min-w-0 flex-1 overflow-y-auto px-4 py-4 text-base leading-7 outline-none sm:min-h-[280px] sm:px-6 sm:py-5",
        "aria-label": uiText("正文"),
      },
      handlePaste(view, event) {
        const clipboard = event.clipboardData
        if (!clipboard) return false
        const html = clipboard.getData("text/html")
        const text = clipboard.getData("text/plain")
        if (!html && !text) return false
        event.preventDefault()
        const content = html ? sanitizeComposerHtml(html) : plainTextToHtml(text)
        const container = document.createElement("div")
        container.innerHTML = content || plainTextToHtml(text)
        const slice = ProseMirrorDOMParser.fromSchema(view.state.schema).parseSlice(container)
        view.dispatch(view.state.tr.replaceSelection(slice).scrollIntoView())
        return true
      },
    },
    onCreate({ editor }) {
      const next = composerValueFromEditor(editor)
      onChange(next)
      setEmpty(!next.text.trim() && !htmlContainsMeaningfulContent(next.html))
    },
    onUpdate({ editor }) {
      dirtyRef.current = true
      const next = composerValueFromEditor(editor)
      onChange(next)
      setEmpty(!next.text.trim() && !htmlContainsMeaningfulContent(next.html))
    },
    onSelectionUpdate() {
      setSelectionVersion((value) => value + 1)
    },
    onTransaction() {
      setSelectionVersion((value) => value + 1)
    },
  })

  React.useEffect(() => {
    if (!editor || editor.isDestroyed) return
    editor.setOptions({
      editorProps: {
        ...editor.options.editorProps,
        attributes: {
          ...(editor.options.editorProps.attributes as Record<string, string>),
          "aria-label": uiText("正文"),
        },
      },
    })
    editor.view.dispatch(editor.state.tr.setMeta("language", composerLanguage))
  }, [editor, composerLanguage])

  React.useEffect(() => {
    if (!editor) return
    const defaultKey = `${defaultValue}\n${defaultHtml || ""}`
    if (defaultKey === lastDefaultRef.current) return
    lastDefaultRef.current = defaultKey
    if (!dirtyRef.current || editor.isEmpty) {
      const next =
        defaultHtml !== undefined
          ? htmlComposerValue(defaultHtml)
          : plainTextComposerValue(defaultValue)
      editor.commands.setContent(next.html || "<p></p>", { emitUpdate: false })
      onChange(next)
      setEmpty(!next.text.trim() && !htmlContainsMeaningfulContent(next.html))
    }
  }, [editor, defaultValue, defaultHtml, onChange])

  const textStyleAttributes = editor?.getAttributes("textStyle") as
    { fontFamily?: string; fontSize?: string; color?: string; backgroundColor?: string } | undefined
  const activeFont = normalizeFontName(textStyleAttributes?.fontFamily || "")
  const activeFontSize = normalizeFontSize(textStyleAttributes?.fontSize || "") || "3"
  const activeColor = textStyleAttributes?.color || ""
  const activeHighlight = textStyleAttributes?.backgroundColor || ""
  void selectionVersion

  function applyFont(font: string) {
    editor?.chain().focus().setFontFamily(font).run()
  }

  function applyFontSize(size: string) {
    const value = composerFontSizeValueByKey[size]
    if (value) editor?.chain().focus().setFontSize(value).run()
  }

  function openInsertDialog(kind: InsertDialogState["kind"]) {
    if (!editor) return
    if (kind === "link") {
      const attrs = editor.getAttributes("link") as { href?: string }
      setInsertDialog({
        kind,
        selectedText: editorTextSelection(editor),
        url: attrs.href || "",
        editing: Boolean(attrs.href),
      })
      return
    }
    const imageAttrs = selectedImageAttributes(editor)
    setInsertDialog({
      kind,
      selectedText: "",
      url: imageAttrs?.src || "",
      alt: imageAttrs?.alt || "",
      editing: Boolean(imageAttrs?.src),
    })
  }

  function confirmInsert(value: InsertDialogValue) {
    if (!editor || !insertDialog) return
    const url = normalizeInsertUrl(value.url, insertDialog.kind)
    if (!url) return
    if (insertDialog.kind === "link") {
      const text = value.text.trim() || insertDialog.selectedText || value.url.trim()
      if (editor.state.selection.empty && !insertDialog.editing) {
        editor
          .chain()
          .focus()
          .insertContent(
            `<a href="${escapeHtml(url)}" target="_blank" rel="noopener noreferrer">${escapeHtml(text)}</a>`
          )
          .run()
      } else {
        if (value.text.trim() && value.text.trim() !== insertDialog.selectedText)
          editor.chain().focus().insertContent(escapeHtml(text)).run()
        editor
          .chain()
          .focus()
          .extendMarkRange("link")
          .setLink({ href: url, target: "_blank", rel: "noopener noreferrer" })
          .run()
      }
      return
    }
    if (insertDialog.editing) {
      editor.chain().focus().updateAttributes("image", { src: url, alt: value.alt.trim() }).run()
      return
    }
    editor.chain().focus().setImage({ src: url, alt: value.alt.trim() }).run()
  }

  function insertSignature() {
    if (!editor || !signatureText.trim()) return
    editor
      .chain()
      .focus()
      .insertContent(`<p><br></p><p>-- <br>${plainTextToHtmlFragment(signatureText)}</p>`)
      .run()
  }

  function insertSchedule(schedule: ScheduleDraft) {
    if (!editor) return
    const normalized = normalizeSchedule(schedule)
    editor
      .chain()
      .focus()
      .insertContent({ type: "scheduleCard", attrs: scheduleToNodeAttributes(normalized) })
      .run()
    onPickFiles([scheduleToFile(normalized)])
  }

  function insertEmoji(emoji: string) {
    editor?.chain().focus().insertContent(emoji).run()
    setEmojiOpen(false)
  }

  function handlePickedFiles(event: React.ChangeEvent<HTMLInputElement>) {
    const nextFiles = Array.from(event.currentTarget.files || [])
    if (nextFiles.length > 0) onPickFiles(nextFiles)
    event.currentTarget.value = ""
  }

  return (
    <div className="flex min-h-[330px] min-w-0 flex-auto shrink-0 flex-col bg-background sm:min-h-[420px]">
      <Input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={handlePickedFiles}
      />
      <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-1 overflow-visible border-b px-3 py-2 sm:px-6">
        <ToolbarButton
          label={uiText("撤销")}
          disabled={!editor?.can().undo()}
          onClick={() => editor?.chain().focus().undo().run()}
        >
          <Undo2 className="h-4 w-4" />
        </ToolbarButton>
        <ToolbarButton
          label={uiText("重做")}
          disabled={!editor?.can().redo()}
          onClick={() => editor?.chain().focus().redo().run()}
        >
          <Redo2 className="h-4 w-4" />
        </ToolbarButton>
        <Separator orientation="vertical" className="mx-2 h-6" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-8 gap-1 rounded-lg px-2 font-normal hover:bg-accent hover:shadow-sm"
              onMouseDown={(event) => event.preventDefault()}
            >
              <Plus className="h-4 w-4" />
              {uiText("插入")}
              <ChevronDown className="h-3.5 w-3.5" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuItem
              className={composerMenuItemClass}
              onSelect={() => fileInputRef.current?.click()}
            >
              <Paperclip className="h-4 w-4" />
              {uiText("附件")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className={composerMenuItemClass}
              onSelect={() => openInsertDialog("link")}
            >
              <Link className="h-4 w-4" />
              {uiText("链接")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className={composerMenuItemClass}
              onSelect={() => openInsertDialog("image")}
            >
              <Image className="h-4 w-4" />
              {uiText("图片链接")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className={composerMenuItemClass}
              onSelect={() => editor?.chain().focus().setHorizontalRule().run()}
            >
              <span className="h-4 w-4 border-t border-current" aria-hidden />
              {uiText("分隔线")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <span className="rounded-lg border px-2 py-1 text-xs text-muted-foreground">
          {uiText("附件 {0}", [maxAttachmentText])}
        </span>
        <ToolbarTextButton
          label={uiText("日程")}
          icon={<Calendar className="h-4 w-4" />}
          onClick={() => setScheduleOpen(true)}
        />
        <DropdownMenu open={emojiOpen} onOpenChange={setEmojiOpen}>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant={emojiOpen ? "secondary" : "ghost"}
              size="sm"
              className={cn(
                "h-8 gap-1.5 rounded-lg px-2 font-normal hover:bg-accent hover:shadow-sm",
                emojiOpen && "border border-primary/30 bg-primary/10 text-primary"
              )}
              onMouseDown={(event) => event.preventDefault()}
            >
              <Smile className="h-4 w-4" />
              {uiText("表情")}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-64 p-2">
            <div className="grid grid-cols-8 gap-1">
              {composerEmojiOptions.map((emoji) => (
                <Button
                  key={emoji}
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 rounded-lg text-lg"
                  onClick={() => insertEmoji(emoji)}
                >
                  {emoji}
                </Button>
              ))}
            </div>
          </DropdownMenuContent>
        </DropdownMenu>
        <ToolbarTextButton
          label={uiText("格式")}
          icon={<Type className="h-4 w-4" />}
          active={formatOpen}
          onClick={() => setFormatOpen((value) => !value)}
        />
        <div className="flex items-center gap-1">
          <ToolbarTextButton
            label={uiText("预览")}
            icon={<Eye className="h-4 w-4" />}
            active={previewOpen}
            onClick={() => setPreviewOpen(true)}
          />
          <ToolbarTextButton
            label={uiText("签名")}
            icon={<Signature className="h-4 w-4" />}
            onClick={insertSignature}
            disabled={!signatureText.trim()}
          />
        </div>
      </div>
      {formatOpen && (
        <div className="flex min-h-14 shrink-0 flex-wrap items-center gap-1 overflow-visible border-b bg-muted/40 px-3 py-2 sm:px-6">
          <ToolbarButton
            label={uiText("清除格式")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().unsetAllMarks().clearNodes().run()}
          >
            <Eraser className="h-4 w-4" />
          </ToolbarButton>
          <Separator orientation="vertical" className="mx-2 h-6" />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className={cn(
                  "h-8 min-w-[112px] justify-between rounded-lg border border-transparent px-2 font-normal hover:border-border hover:bg-accent hover:shadow-sm",
                  activeFont && "border-primary/35 bg-primary/10 text-primary shadow-sm"
                )}
                onMouseDown={(event) => event.preventDefault()}
                disabled={!editor}
              >
                <span className="truncate">{uiText(fontLabel(activeFont))}</span>
                <ChevronDown className="h-3.5 w-3.5" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {composerFontOptions.map((font) => (
                <DropdownMenuItem
                  key={font}
                  className={composerMenuItemClass}
                  onSelect={() => applyFont(font)}
                >
                  <Check
                    className={cn("h-4 w-4", activeFont === font ? "opacity-100" : "opacity-0")}
                  />
                  <span style={{ fontFamily: font }}>{font}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className={cn(
                  "h-8 min-w-[84px] justify-between rounded-lg border border-transparent px-2 font-normal hover:border-border hover:bg-accent hover:shadow-sm",
                  activeFontSize !== "3" && "border-primary/35 bg-primary/10 text-primary shadow-sm"
                )}
                onMouseDown={(event) => event.preventDefault()}
                disabled={!editor}
              >
                {uiText(fontSizeLabel(activeFontSize))}
                <ChevronDown className="h-3.5 w-3.5" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {composerFontSizeOptions.map(([size, label]) => (
                <DropdownMenuItem
                  key={size}
                  className={composerMenuItemClass}
                  onSelect={() => applyFontSize(size)}
                >
                  <Check
                    className={cn("h-4 w-4", activeFontSize === size ? "opacity-100" : "opacity-0")}
                  />
                  {uiText(label)}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <Separator orientation="vertical" className="mx-2 h-6" />
          <ToolbarButton
            label={uiText("加粗")}
            active={editor?.isActive("bold")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleBold().run()}
          >
            <Bold className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("斜体")}
            active={editor?.isActive("italic")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleItalic().run()}
          >
            <Italic className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("下划线")}
            active={editor?.isActive("underline")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleUnderline().run()}
          >
            <Underline className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("删除线")}
            active={editor?.isActive("strike")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleStrike().run()}
          >
            <Strikethrough className="h-4 w-4" />
          </ToolbarButton>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className={cn(
                  "h-8 w-8 rounded-lg border border-transparent text-muted-foreground hover:border-border hover:bg-accent hover:text-foreground hover:shadow-sm",
                  activeColor && "border-primary/35 bg-primary/10 text-primary shadow-sm"
                )}
                title={uiText("文字颜色")}
                aria-label={uiText("文字颜色")}
                onMouseDown={(event) => event.preventDefault()}
                disabled={!editor}
              >
                <Type className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-36">
              {composerTextColors.map(([color, label]) => (
                <DropdownMenuItem
                  key={color}
                  className={composerMenuItemClass}
                  onSelect={() =>
                    color === "#111827"
                      ? editor?.chain().focus().unsetColor().run()
                      : editor?.chain().focus().setColor(color).run()
                  }
                >
                  <Check
                    className={cn(
                      "h-4 w-4",
                      activeColor === color || (!activeColor && color === "#111827")
                        ? "opacity-100"
                        : "opacity-0"
                    )}
                  />
                  <span
                    className="h-3 w-3 rounded-full border"
                    style={{ backgroundColor: color }}
                  />
                  {uiText(label)}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className={cn(
                  "h-8 w-8 rounded-lg border border-transparent text-muted-foreground hover:border-border hover:bg-accent hover:text-foreground hover:shadow-sm",
                  activeHighlight && "border-primary/35 bg-primary/10 text-primary shadow-sm"
                )}
                title={uiText("高亮")}
                aria-label={uiText("高亮")}
                onMouseDown={(event) => event.preventDefault()}
                disabled={!editor}
              >
                <Highlighter className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {composerHighlightColors.map(([color, label]) => (
                <DropdownMenuItem
                  key={color}
                  className={composerMenuItemClass}
                  onSelect={() =>
                    color === "transparent"
                      ? editor?.chain().focus().unsetBackgroundColor().run()
                      : editor?.chain().focus().setBackgroundColor(color).run()
                  }
                >
                  <Check
                    className={cn(
                      "h-4 w-4",
                      activeHighlight === color || (!activeHighlight && color === "transparent")
                        ? "opacity-100"
                        : "opacity-0"
                    )}
                  />
                  <span
                    className="h-3 w-3 rounded-full border"
                    style={{ backgroundColor: color }}
                  />
                  {uiText(label)}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <Separator orientation="vertical" className="mx-2 h-6" />
          <ToolbarButton
            label={uiText("无序列表")}
            active={editor?.isActive("bulletList")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleBulletList().run()}
          >
            <List className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("有序列表")}
            active={editor?.isActive("orderedList")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleOrderedList().run()}
          >
            <ListOrdered className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("减少缩进")}
            disabled={!editor?.can().liftListItem("listItem")}
            onClick={() => editor?.chain().focus().liftListItem("listItem").run()}
          >
            <IndentDecrease className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("增加缩进")}
            disabled={!editor?.can().sinkListItem("listItem")}
            onClick={() => editor?.chain().focus().sinkListItem("listItem").run()}
          >
            <IndentIncrease className="h-4 w-4" />
          </ToolbarButton>
          <Separator orientation="vertical" className="mx-2 h-6" />
          <ToolbarButton
            label={uiText("左对齐")}
            active={editor?.isActive({ textAlign: "left" })}
            disabled={!editor}
            onClick={() => editor?.chain().focus().setTextAlign("left").run()}
          >
            <AlignLeft className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("居中")}
            active={editor?.isActive({ textAlign: "center" })}
            disabled={!editor}
            onClick={() => editor?.chain().focus().setTextAlign("center").run()}
          >
            <AlignCenter className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("右对齐")}
            active={editor?.isActive({ textAlign: "right" })}
            disabled={!editor}
            onClick={() => editor?.chain().focus().setTextAlign("right").run()}
          >
            <AlignRight className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("引用")}
            active={editor?.isActive("blockquote")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleBlockquote().run()}
          >
            <Quote className="h-4 w-4" />
          </ToolbarButton>
          <ToolbarButton
            label={uiText("代码块")}
            active={editor?.isActive("codeBlock")}
            disabled={!editor}
            onClick={() => editor?.chain().focus().toggleCodeBlock().run()}
          >
            <Code2 className="h-4 w-4" />
          </ToolbarButton>
        </div>
      )}
      <div
        className={cn(
          "composer-editor relative flex min-h-[240px] min-w-0 flex-1 border-b focus-within:bg-card/40 sm:min-h-[280px]",
          "[&_.ProseMirror]:min-h-[240px] [&_.ProseMirror]:w-full [&_.ProseMirror]:flex-1 [&_.ProseMirror]:overflow-y-auto [&_.ProseMirror]:px-4 [&_.ProseMirror]:py-4 [&_.ProseMirror]:text-base [&_.ProseMirror]:leading-7 [&_.ProseMirror]:outline-none sm:[&_.ProseMirror]:min-h-[280px] sm:[&_.ProseMirror]:px-6 sm:[&_.ProseMirror]:py-5",
          "[&_.ProseMirror_p.is-editor-empty:first-child::before]:pointer-events-none [&_.ProseMirror_p.is-editor-empty:first-child::before]:float-left [&_.ProseMirror_p.is-editor-empty:first-child::before]:h-0 [&_.ProseMirror_p.is-editor-empty:first-child::before]:text-muted-foreground [&_.ProseMirror_p.is-editor-empty:first-child::before]:content-[attr(data-placeholder)]",
          "[&_.ProseMirror_ul]:list-disc [&_.ProseMirror_ol]:list-decimal [&_.ProseMirror_ul]:pl-6 [&_.ProseMirror_ol]:pl-6 [&_.ProseMirror_blockquote]:border-l-4 [&_.ProseMirror_blockquote]:border-border [&_.ProseMirror_blockquote]:pl-4 [&_.ProseMirror_blockquote]:text-muted-foreground [&_.ProseMirror_pre]:rounded-lg [&_.ProseMirror_pre]:bg-muted [&_.ProseMirror_pre]:p-3",
          empty && "bg-background"
        )}
      >
        <EditorContent editor={editor} className="flex min-h-0 min-w-0 flex-1" />
      </div>
      {quotedHtml && (
        <div className="border-b bg-muted/20">
          <div className="flex items-center justify-between gap-2 px-4 py-2 text-xs text-muted-foreground sm:px-6">
            <span className="truncate">{uiText("以下是原邮件，发送时原样附带（只读）")}</span>
            {onRemoveQuote && (
              <button
                type="button"
                className="shrink-0 rounded px-1.5 py-0.5 hover:bg-accent hover:text-foreground"
                onClick={onRemoveQuote}
              >
                {uiText("删除")}
              </button>
            )}
          </div>
          <div className="max-h-[360px] overflow-y-auto overflow-x-hidden border-t bg-white px-2 py-2 sm:px-4">
            <MailHtmlFrame
              key={quotedHtml.slice(0, 64)}
              bodyHtml={quotedHtml}
              bodyText={quotedText || ""}
              minHeight={60}
            />
          </div>
        </div>
      )}
      {files.length > 0 && (
        <div className="border-t px-4 py-3 sm:px-6">
          <div className="flex flex-wrap gap-2">
            {files.map((file, index) => (
              <Badge
                key={`${file.name}-${file.size}-${index}`}
                variant="outline"
                className="h-8 gap-2 rounded-lg px-2 font-normal"
              >
                <Paperclip className="h-3.5 w-3.5" />
                <span className="max-w-48 truncate">{file.name}</span>
                <span className="text-muted-foreground">{formatBytes(file.size)}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5 rounded-lg"
                  onClick={() => onRemoveFile(index)}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </Badge>
            ))}
          </div>
        </div>
      )}
      <InsertContentDialog
        state={insertDialog}
        onOpenChange={(open) => {
          if (!open) setInsertDialog(null)
        }}
        onConfirm={confirmInsert}
      />
      <ScheduleDialog
        open={scheduleOpen}
        onOpenChange={setScheduleOpen}
        onConfirm={(schedule) => {
          insertSchedule(schedule)
          setScheduleOpen(false)
        }}
      />
      <Dialog open={previewOpen} onOpenChange={setPreviewOpen}>
        <DialogContent className="w-[min(92vw,44rem)] max-w-none">
          <DialogHeader>
            <DialogTitle>{uiText("邮件预览")}</DialogTitle>
          </DialogHeader>
          <div
            className="mail-html max-h-[60vh] overflow-y-auto rounded-lg border bg-background p-5 text-sm leading-7"
            dangerouslySetInnerHTML={{
              __html: sanitizeComposerHtml(editor?.getHTML() || "") || "<p></p>",
            }}
          />
        </DialogContent>
      </Dialog>
    </div>
  )
}

const durationOptions = [
  { value: "15", label: "15分钟" },
  { value: "30", label: "30分钟" },
  { value: "60", label: "1小时" },
  { value: "120", label: "2小时" },
  { value: "1440", label: "1天" },
]
const reminderOptions = [
  { value: "0", label: "准时" },
  { value: "5", label: "5分钟前" },
  { value: "15", label: "15分钟前" },
  { value: "30", label: "30分钟前" },
  { value: "60", label: "1小时前" },
  { value: "1440", label: "1天前" },
]
const repeatOptions = [
  { value: "none", label: "永不" },
  { value: "daily", label: "每天" },
  { value: "weekly", label: "每周" },
  { value: "monthly", label: "每月" },
  { value: "yearly", label: "每年" },
] as const

function ScheduleDialog({
  open,
  onOpenChange,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: (schedule: ScheduleDraft) => void
}) {
  useUiLanguage()

  const [duration, setDuration] = React.useState("60")
  const [reminder, setReminder] = React.useState("15")
  const [repeat, setRepeat] = React.useState<ScheduleDraft["repeat"]>("none")
  const [allDay, setAllDay] = React.useState(false)
  const [customDuration, setCustomDuration] = React.useState(false)
  const [customReminder, setCustomReminder] = React.useState(false)
  const [lunar, setLunar] = React.useState(false)
  const defaultStart = open ? defaultScheduleStartValue() : ""
  const { toast } = useToast()

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const title = String(form.get("title") || "").trim()
    if (!title) {
      toast({ title: "请输入日程主题" })
      return
    }
    const durationMinutes = customDuration
      ? Number(form.get("customDuration") || 60)
      : Number(duration)
    const reminderMinutes = customReminder
      ? Number(form.get("customReminder") || 15)
      : Number(reminder)
    onConfirm({
      title,
      start: String(form.get("start") || defaultStart),
      durationMinutes: Math.max(1, durationMinutes || 60),
      reminderMinutes: Math.max(0, reminderMinutes || 0),
      repeat,
      allDay,
      customDuration,
      customReminder,
      lunar,
      location: String(form.get("location") || ""),
      description: String(form.get("description") || ""),
    })
    event.currentTarget.reset()
    setDuration("60")
    setReminder("15")
    setRepeat("none")
    setAllDay(false)
    setCustomDuration(false)
    setCustomReminder(false)
    setLunar(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(92vw,36rem)] max-w-none">
        <DialogHeader>
          <DialogTitle>{uiText("新建日程")}</DialogTitle>
        </DialogHeader>
        <form className="space-y-5" onSubmit={submit}>
          <Input
            name="title"
            placeholder={uiText("输入日程主题")}
            className="h-11 border-0 border-b px-0 text-lg shadow-none focus-visible:ring-0"
          />
          <div className="grid gap-4">
            <ScheduleRow label={uiText("开始")}>
              <Input
                name="start"
                type={allDay ? "date" : "datetime-local"}
                defaultValue={allDay ? defaultStart.slice(0, 10) : defaultStart}
                className="h-11"
              />
              <CheckLabel
                id="schedule-all-day"
                label={uiText("全天")}
                checked={allDay}
                onCheckedChange={setAllDay}
              />
            </ScheduleRow>
            <ScheduleRow label={uiText("持续")}>
              {customDuration ? (
                <Input
                  name="customDuration"
                  type="number"
                  min={1}
                  defaultValue="60"
                  className="h-11"
                />
              ) : (
                <Select value={duration} onValueChange={setDuration}>
                  <SelectTrigger className="h-11">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {durationOptions.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {uiText(item.label)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <CheckLabel
                id="schedule-custom-duration"
                label={uiText("自定义")}
                checked={customDuration}
                onCheckedChange={setCustomDuration}
              />
            </ScheduleRow>
            <ScheduleRow label={uiText("提醒")}>
              {customReminder ? (
                <Input
                  name="customReminder"
                  type="number"
                  min={0}
                  defaultValue="15"
                  className="h-11"
                />
              ) : (
                <Select value={reminder} onValueChange={setReminder}>
                  <SelectTrigger className="h-11">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {reminderOptions.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {uiText(item.label)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <CheckLabel
                id="schedule-custom-reminder"
                label={uiText("自定义")}
                checked={customReminder}
                onCheckedChange={setCustomReminder}
              />
            </ScheduleRow>
            <ScheduleRow label={uiText("重复")}>
              <Select
                value={repeat}
                onValueChange={(value) => setRepeat(value as ScheduleDraft["repeat"])}
              >
                <SelectTrigger className="h-11">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {repeatOptions.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {uiText(item.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <CheckLabel
                id="schedule-lunar"
                label={uiText("农历")}
                checked={lunar}
                onCheckedChange={setLunar}
              />
            </ScheduleRow>
            <ScheduleRow label={uiText("位置")}>
              <Input name="location" placeholder={uiText("请输入位置")} className="h-11" />
            </ScheduleRow>
            <ScheduleRow label={uiText("描述")}>
              <Input name="description" placeholder={uiText("输入描述")} className="h-11" />
            </ScheduleRow>
          </div>
          <DialogFooter>
            <Button type="submit">{uiText("确定")}</Button>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {uiText("取消")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ScheduleRow({ label, children }: { label: string; children: React.ReactNode }) {
  useUiLanguage()

  return (
    <div className="grid gap-2 sm:grid-cols-[3rem_minmax(0,1fr)_5.5rem] sm:items-center">
      <Label className="text-base font-normal">{uiText(label)}</Label>
      {children}
    </div>
  )
}

function CheckLabel({
  id,
  label,
  checked,
  onCheckedChange,
}: {
  id: string
  label: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  useUiLanguage()

  return (
    <div className="flex items-center gap-2">
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={(value) => onCheckedChange(value === true)}
      />
      <Label htmlFor={id} className="cursor-pointer text-sm font-normal">
        {uiText(label)}
      </Label>
    </div>
  )
}

function ToolbarButton({
  label,
  children,
  active,
  onClick,
  disabled,
}: {
  label: string
  children: React.ReactNode
  active?: boolean
  onClick?: () => void
  disabled?: boolean
}) {
  useUiLanguage()

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className={cn(
        "h-8 w-8 rounded-lg border border-transparent text-muted-foreground transition-all hover:border-border hover:bg-accent hover:text-foreground hover:shadow-sm",
        active &&
          "border-primary/35 bg-primary/10 text-primary shadow-sm hover:bg-primary/15 hover:text-primary"
      )}
      title={uiText(label)}
      aria-label={uiText(label)}
      aria-pressed={active || undefined}
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
      disabled={disabled}
    >
      {children}
    </Button>
  )
}

function splitEmails(s: string) {
  // 不能按空白直接 split：`John Doe <john@example.com>` 会被拆成三段。
  // 逗号/分号永远分隔；空白只在当前已是一整个地址时才分隔。
  const out: string[] = []
  let current = ""
  let quote = false
  let angle = 0
  const push = () => {
    const value = current.trim()
    if (value) out.push(value)
    current = ""
  }
  for (const ch of s) {
    if (ch === '"') {
      quote = !quote
      current += ch
      continue
    }
    if (!quote && ch === "<") angle += 1
    if (!quote && ch === ">" && angle > 0) angle -= 1
    if (!quote && angle === 0 && (ch === "," || ch === ";" || ch === "，")) {
      push()
      continue
    }
    if (!quote && angle === 0 && /\s/.test(ch)) {
      if (!current.includes("@")) {
        current += " "
      } else if (current.includes(">") || !current.includes("<")) {
        push()
      } else {
        current += " "
      }
      continue
    }
    current += ch
  }
  push()
  return out
}

function attachmentLimitBytes(limits?: PermissionLimits) {
  const mb = limits?.maxAttachmentMb || 0
  return mb > 0 ? mb * 1024 * 1024 : 0
}
async function fileToAttachment(file: File) {
  const buffer = await file.arrayBuffer()
  let binary = ""
  const bytes = new Uint8Array(buffer)
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i])
  return {
    filename: file.name,
    contentType: file.type || "application/octet-stream",
    contentBase64: btoa(binary),
  }
}
