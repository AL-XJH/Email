import {
  uiMessage,
  type UiText,
  uiText,
  useLanguage as useUiLanguage,
  Language,
  useLanguage,
} from "@/lib/language"
import { errorMessage } from "@/lib/ui-errors"
import { copyToClipboard } from "@/lib/clipboard"

import * as React from "react"
import {
  type InfiniteData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useNavigate, useSearchParams } from "react-router-dom"
import type { ImperativePanelHandle } from "react-resizable-panels"
import {
  Archive,
  ArrowLeft,
  Ban,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsUpDown,
  Clock3,
  Copy,
  Ellipsis,
  Forward,
  History,
  Inbox,
  Mail,
  MailCheck,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  PencilLine,
  Plus,
  RefreshCcw,
  Reply,
  RotateCcw,
  Search,
  Send,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  Star,
  Sun,
  Trash2,
  X,
} from "lucide-react"
import {
  api,
  ExternalImapAccount,
  ListResponse,
  Mailbox,
  MailFolder,
  MailLabel,
  MailMessage,
  ScheduledSend,
  SendQueueItem,
  SendQueueAuditEvent,
  SendQueueStatus,
} from "@/lib/api"
import { cn, formatBytes, formatDateTime } from "@/lib/utils"
import { applyTheme, getInitialTheme } from "@/lib/theme"
import { useDisplayMode } from "@/lib/display-mode"

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
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { Label } from "@/components/ui/label"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { quotedComposerValue, quotedHtmlComposerValue, withPrefix } from "@/components/mail-content"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from "@/components/ui/sidebar"
import { MailAttachment } from "@/components/mail-attachment"
import { useMe } from "@/hooks/use-me"
import { useIsMobile } from "@/hooks/use-mobile"
import { useDebouncedValue } from "@/lib/use-debounced-value"
import {
  accountInitial,
  attachmentHref,
  cleanAccountName,
  labelDotColor,
  senderAddress,
  senderDisplayName,
  senderTitle,
} from "@/lib/mail-format"
import { CompactMessageRow, MessageRow } from "@/components/mail-message-row"

import type { ComposeDraft } from "@/lib/compose-types"

const ComposeDialog = React.lazy(() =>
  import("@/components/mail-compose-dialog").then((mod) => ({ default: mod.ComposeDialog }))
)
import {
  CompactMessageDetail,
  MessageMetaPanel,
  NewLabelButton,
  TranslatableMailBody,
} from "@/components/mail-detail"
import { useToast } from "@/hooks/use-toast"
import { hasPermission } from "@/lib/permissions"

// 标签颜色优先用服务端存的值（seek.li 就是这么做的），缺失时才按名字哈希兜底。
const folderIcons: Record<string, React.ReactNode> = {
  inbox: <Inbox className="h-4 w-4" />,
  sent: <Send className="h-4 w-4" />,
  drafts: <Pencil className="h-4 w-4" />,
  archive: <Archive className="h-4 w-4" />,
  spam: <Ban className="h-4 w-4" />,
  trash: <Trash2 className="h-4 w-4" />,
}
const folderLabels: Record<string, string> = {
  Inbox: "收件箱",
  Sent: "已发送",
  Drafts: "草稿箱",
  Archive: "已归档",
  Spam: "垃圾邮件",
  Trash: "已删除",
}

type MailFilter = "all" | "unread" | "starred" | "attachments"
type MailView = "folder" | "starred" | "label" | "scheduled" | "sendQueue" | "external"
type MailListResponse = { items?: MailMessage[]; nextCursor?: string; totalCount?: number }
type MailPageSize = 20 | 30 | 50
type PendingConfirm = {
  title: UiText
  description?: UiText
  confirmText: string
  onConfirm: () => void
}
type MailNotificationState = { latestId: string; latestReceivedAt: string }
type MessageContextMenuState = { message: MailMessage; x: number; y: number }
type SidebarContextMenuState = { item: MailMenuItem; x: number; y: number }
type FolderDropTarget = { key: string; edge: "before" | "after" | "end" }

const MAIL_PAGE_SIZE_KEY = "lanqin:mail-page-size"

function getInitialMailPageSize(): MailPageSize {
  const stored = Number(localStorage.getItem(MAIL_PAGE_SIZE_KEY))
  return stored === 20 || stored === 50 ? stored : 30
}

async function runWithConcurrency<T>(
  items: readonly T[],
  limit: number,
  task: (item: T, index: number) => Promise<unknown>
) {
  let nextIndex = 0
  let hasError = false
  let firstError: unknown
  const workerCount = Math.min(Math.max(1, limit), items.length)
  const workers = Array.from({ length: workerCount }, async () => {
    while (!hasError) {
      const index = nextIndex++
      if (index >= items.length) return
      try {
        await task(items[index], index)
      } catch (error) {
        hasError = true
        firstError = error
      }
    }
  })
  await Promise.all(workers)
  if (hasError) throw firstError
}

type MailMenuItem =
  | {
      type: "starred"
      key: string
      label: string
      icon: React.ReactNode
      count: number
      order: number
    }
  | {
      type: "scheduled"
      key: string
      label: string
      icon: React.ReactNode
      count: number
      order: number
    }
  | {
      type: "sendQueue"
      key: string
      label: string
      icon: React.ReactNode
      count: number
      order: number
    }
  | {
      type: "folder"
      key: string
      folderId: string
      folderName: string
      label: string
      icon: React.ReactNode
      count: number
      custom: boolean
      order: number
    }

const filterLabels: Record<MailFilter, string> = {
  all: "全部邮件",
  unread: "未读邮件",
  starred: "星标邮件",
  attachments: "有附件",
}

export function MailPage() {
  const qc = useQueryClient()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [urlParams, setURLParams] = useSearchParams()
  const deepLinkMailboxId = urlParams.get("mailboxId") || ""
  const deepLinkMessageId = urlParams.get("messageId") || ""
  const me = useMe()
  const [folder, setFolder] = React.useState("Inbox")
  const [mailView, setMailView] = React.useState<MailView>("folder")
  const [selectedLabelId, setSelectedLabelId] = React.useState("")
  const [query, setQuery] = React.useState("")
  // 搜索框输入即时回显，但只有停顿 300ms 才真的去请求，避免每敲一个字符打一次接口。
  const debouncedQuery = useDebouncedValue(query, 300)
  const [messagePageIndex, setMessagePageIndex] = React.useState(0)
  const [messagePageSize, setMessagePageSize] = React.useState<MailPageSize>(getInitialMailPageSize)
  const [selectedId, setSelectedId] = React.useState<string | null>(null)
  const [compactSelectedIds, setCompactSelectedIds] = React.useState<string[]>([])
  const [composeOpen, setComposeOpen] = React.useState(false)
  const [composeDraft, setComposeDraft] = React.useState<ComposeDraft | undefined>()
  const [sidebarCollapsed, setSidebarCollapsed] = React.useState(false)
  const [mailFilter, setMailFilter] = React.useState<MailFilter>("all")
  const [selectedMailboxId, setSelectedMailboxId] = React.useState(
    () => localStorage.getItem("lanqin:selected-mailbox") || ""
  )
  const [selectedExternalAccountId, setSelectedExternalAccountId] = React.useState("")
  const [expandedExternalAccountIds, setExpandedExternalAccountIds] = React.useState<string[]>([])
  const [externalFolder, setExternalFolder] = React.useState("INBOX")
  const [darkMode, setDarkMode] = React.useState(getInitialTheme)
  const [language] = useLanguage()
  const [displayMode] = useDisplayMode()
  const isMobile = useIsMobile()

  // 手机端读邮件时往 history 里压一条记录：
  // 从屏幕两侧滑一下触发的是浏览器「返回」，没有这条记录就会直接退出网页。
  const readerHistoryPushed = React.useRef(false)
  React.useEffect(() => {
    if (!isMobile || !selectedId) return
    if (
      readerHistoryPushed.current ||
      (window.history.state as { lanqinReader?: boolean } | null)?.lanqinReader
    )
      return
    window.history.pushState({ ...(window.history.state || {}), lanqinReader: true }, "")
    readerHistoryPushed.current = true
  }, [isMobile, selectedId])

  React.useEffect(() => {
    if (!isMobile) return
    function handleReaderPopState() {
      if ((window.history.state as { lanqinReader?: boolean } | null)?.lanqinReader) return
      // 我们压的那条被弹掉了 —— 回到列表，页面不离开。
      readerHistoryPushed.current = false
      setSelectedId(null)
    }
    window.addEventListener("popstate", handleReaderPopState)
    return () => window.removeEventListener("popstate", handleReaderPopState)
  }, [isMobile])

  // 点左上角返回也走 history，避免留下一条「幽灵」记录让下次返回卡住。
  function closeReader() {
    if (isMobile && (window.history.state as { lanqinReader?: boolean } | null)?.lanqinReader) {
      window.history.back()
      return
    }
    setSelectedId(null)
  }
  const [refreshing, setRefreshing] = React.useState(false)
  const [listAutoRefreshing, setListAutoRefreshing] = React.useState(false)
  const [bulkPending, setBulkPending] = React.useState(false)
  const [pendingConfirm, setPendingConfirm] = React.useState<PendingConfirm | null>(null)
  const [cancelingScheduledId, setCancelingScheduledId] = React.useState("")
  const [sendQueueStatus, setSendQueueStatus] = React.useState<SendQueueStatus | "all">("all")
  const [sendQueueMessageId, setSendQueueMessageId] = React.useState("")
  const [sendQueueRecipient, setSendQueueRecipient] = React.useState("")
  const [sendQueueFrom, setSendQueueFrom] = React.useState("")
  const [sendQueueTo, setSendQueueTo] = React.useState("")
  const [sendQueueAuditId, setSendQueueAuditId] = React.useState("")
  const [sendQueuePendingId, setSendQueuePendingId] = React.useState("")
  const [mobileSidebarOpen, setMobileSidebarOpen] = React.useState(false)
  const [labelEditMode, setLabelEditMode] = React.useState(false)
  const [labelsCollapsed, setLabelsCollapsed] = React.useState(false)
  const [foldersCollapsed, setFoldersCollapsed] = React.useState(false)
  const [newLabelEditing, setNewLabelEditing] = React.useState(false)
  const [messageContextMenu, setMessageContextMenu] =
    React.useState<MessageContextMenuState | null>(null)
  const [sidebarContextMenu, setSidebarContextMenu] =
    React.useState<SidebarContextMenuState | null>(null)
  const [folderDialogOpen, setFolderDialogOpen] = React.useState(false)
  const [draggingFolderId, setDraggingFolderId] = React.useState("")
  const [folderDropTarget, setFolderDropTarget] = React.useState<FolderDropTarget | null>(null)
  const sidebarPanelRef = React.useRef<ImperativePanelHandle>(null)
  const themeMountedRef = React.useRef(false)
  const mailNotifyStateRef = React.useRef<Record<string, MailNotificationState>>({})
  const mailAudioContextRef = React.useRef<AudioContext | null>(null)
  const user = me.data?.user
  const canAccessMail = hasPermission(user, "mail.access")
  const canReadMail = hasPermission(user, "mail.messages.read")
  const canSendMail = hasPermission(user, "mail.messages.send")
  const canManageDrafts = hasPermission(user, "mail.messages.drafts")
  const canScheduleMail = hasPermission(user, "mail.messages.schedule")
  const canOrganizeMail = hasPermission(user, "mail.messages.organize")
  const canManageBlocked = hasPermission(user, "mail.blocked_senders.manage")
  const canManageLabels = hasPermission(user, "mail.labels.manage")
  const hasAttachmentPermission = hasPermission(user, "mail.attachments.download")
  const canManageSignatures = hasPermission(user, "mail.signatures.manage")
  const publicSettings = useQuery({ queryKey: ["public-settings"], queryFn: api.publicSettings })
  const externalImapEnabled = publicSettings.data?.externalImapEnabled ?? false

  const mailboxList = useQuery({
    queryKey: ["mailboxes", "mine"],
    queryFn: api.myMailboxes,
    enabled: canAccessMail,
  })
  const externalMailAccounts = useQuery({
    queryKey: ["mail-external-accounts"],
    queryFn: api.externalMailAccounts,
    enabled: canAccessMail && canReadMail && externalImapEnabled,
  })
  const selectedExternalAccount = React.useMemo(
    () =>
      externalImapEnabled
        ? externalMailAccounts.data?.items.find((item) => item.id === selectedExternalAccountId)
        : undefined,
    [externalImapEnabled, externalMailAccounts.data?.items, selectedExternalAccountId]
  )
  const externalFolders = useQuery({
    queryKey: ["mail-external-folders", selectedExternalAccountId],
    queryFn: () => api.externalFolders(selectedExternalAccountId),
    enabled: !!selectedExternalAccountId && canReadMail && externalImapEnabled,
  })
  const externalMessagesStoredLocally = selectedExternalAccount?.storageMode === "local"
  const selectedMailbox = React.useMemo(() => {
    const items = mailboxList.data?.items || []
    return items.find((item) => item.id === selectedMailboxId) || items[0]
  }, [mailboxList.data?.items, selectedMailboxId])
  const activeMailboxId = selectedMailbox?.id || ""
  const selectedMailboxOwned = selectedMailbox?.access !== "read"
  const canDownloadAttachments =
    hasAttachmentPermission &&
    (selectedMailboxOwned || selectedMailbox?.shareAllowsAttachments !== false)
  const canSendSelectedMailbox = canSendMail && selectedMailboxOwned
  const canManageSelectedDrafts = canManageDrafts && selectedMailboxOwned
  const canScheduleSelectedMailbox = canScheduleMail && selectedMailboxOwned
  const canOrganizeSelectedMailbox = canOrganizeMail && selectedMailboxOwned
  const canManageSelectedLabels =
    canManageLabels && selectedMailboxOwned && (mailView !== "external" || externalMessagesStoredLocally)
  const restrictedSharedMailbox =
    selectedMailbox?.access === "read" && selectedMailbox.shareScope === "custom"
  const hasMailboxes = (mailboxList.data?.items.length || 0) > 0
  const folders = useQuery({
    queryKey: ["folders", activeMailboxId],
    queryFn: () => api.folders(activeMailboxId),
    enabled: !!activeMailboxId && canReadMail,
  })
  const labels = useQuery({
    queryKey: ["labels", activeMailboxId],
    queryFn: () => api.labels(activeMailboxId),
    enabled: !!activeMailboxId && (canReadMail || canManageLabels),
  })
  const sharedSelectionReady =
    !restrictedSharedMailbox ||
    mailView === "external" ||
    (mailView === "folder" && !!folders.data?.items.some((item) => item.name === folder)) ||
    (mailView === "label" && !!labels.data?.items.some((item) => item.id === selectedLabelId)) ||
    (mailView === "starred" && !!selectedMailbox?.shareIncludesStarred)
  const mailStats = useQuery({
    queryKey: ["mail-stats", activeMailboxId],
    queryFn: () => api.mailStats(activeMailboxId),
    enabled: !!activeMailboxId && selectedMailboxOwned && hasPermission(user, "mail.stats.view"),
  })
  const scheduledSends = useQuery({
    queryKey: ["scheduled-sends", activeMailboxId],
    queryFn: () => api.scheduledSends(activeMailboxId),
    enabled: !!activeMailboxId && canScheduleSelectedMailbox,
    refetchInterval: mailView === "scheduled" ? 30000 : false,
  })
  const canViewSendQueue = canReadMail && selectedMailboxOwned
  // 这两个是逐字符输入的过滤框，不防抖的话每敲一个字母就打一次发信队列接口。
  const sendQueueMessageIdFilter = useDebouncedValue(sendQueueMessageId, 300)
  const sendQueueRecipientFilter = useDebouncedValue(sendQueueRecipient, 300)
  const sendQueue = useQuery({
    queryKey: [
      "send-queue",
      activeMailboxId,
      sendQueueStatus,
      sendQueueMessageIdFilter,
      sendQueueRecipientFilter,
      sendQueueFrom,
      sendQueueTo,
    ],
    queryFn: () =>
      api.sendQueue({
        mailboxId: activeMailboxId,
        status: sendQueueStatus,
        messageId: sendQueueMessageIdFilter.trim(),
        recipient: sendQueueRecipientFilter.trim(),
        from: datetimeLocalToISO(sendQueueFrom),
        to: datetimeLocalToISO(sendQueueTo),
      }),
    enabled: !!activeMailboxId && canViewSendQueue,
    refetchInterval: mailView === "sendQueue" ? 15000 : false,
  })
  const sendQueueAudit = useQuery({
    queryKey: ["send-queue-audit", sendQueueAuditId],
    queryFn: () => api.sendQueueAudit(sendQueueAuditId),
    enabled: !!sendQueueAuditId && canViewSendQueue,
  })
  const mailRefreshInterval = publicSettings.data?.mailAutoRefresh
    ? Math.max(publicSettings.data.mailRefreshMs || 30000, 5000)
    : false
  const isPlainInboxView = mailView === "folder" && folder === "Inbox" && query.trim() === ""
  React.useEffect(() => {
    if (externalImapEnabled) return
    setSelectedExternalAccountId("")
    setExpandedExternalAccountIds([])
    if (mailView === "external") setMailView("folder")
  }, [externalImapEnabled, mailView])
  React.useEffect(() => {
    const ids = new Set((externalMailAccounts.data?.items || []).map((item) => item.id))
    setExpandedExternalAccountIds((current) => {
      const next = current.filter((id) => ids.has(id))
      return next.length === current.length ? current : next
    })
  }, [externalMailAccounts.data?.items])
  React.useEffect(() => {
    if (!restrictedSharedMailbox || !folders.isSuccess || !labels.isSuccess) return
    if (mailView === "folder" && folders.data.items.some((item) => item.name === folder)) return
    if (mailView === "label" && labels.data.items.some((item) => item.id === selectedLabelId))
      return
    if (mailView === "starred" && selectedMailbox?.shareIncludesStarred) return
    const firstFolder = folders.data.items[0]
    if (firstFolder) {
      setMailView("folder")
      setFolder(firstFolder.name)
      setSelectedLabelId("")
    } else if (labels.data.items[0]) {
      setMailView("label")
      setSelectedLabelId(labels.data.items[0].id)
    } else if (selectedMailbox?.shareIncludesStarred) {
      setMailView("starred")
      setSelectedLabelId("")
    }
    setSelectedId(null)
  }, [
    folder,
    folders.data?.items,
    folders.isSuccess,
    labels.data?.items,
    labels.isSuccess,
    mailView,
    restrictedSharedMailbox,
    selectedLabelId,
    selectedMailbox?.shareIncludesStarred,
  ])
  const inboxProbe = useQuery({
    queryKey: ["mail-notifications", activeMailboxId],
    queryFn: () => api.messages("Inbox", "", "", activeMailboxId),
    enabled:
      !!activeMailboxId &&
      canReadMail &&
      !isPlainInboxView &&
      (!restrictedSharedMailbox || !!folders.data?.items.some((item) => item.name === "Inbox")),
    refetchInterval: mailRefreshInterval,
    refetchIntervalInBackground: true,
  })
  const messages = useInfiniteQuery({
    queryKey: [
      "messages",
      activeMailboxId,
      mailView,
      folder,
      selectedLabelId,
      debouncedQuery,
      messagePageSize,
    ],
    queryFn: ({ pageParam }) => {
      const cursor = typeof pageParam === "string" ? pageParam : ""
      if (mailView === "starred")
        return api.starredMessages(debouncedQuery, cursor, activeMailboxId, messagePageSize)
      if (mailView === "label")
        return api.labelMessages(
          selectedLabelId,
          debouncedQuery,
          cursor,
          activeMailboxId,
          messagePageSize
        )
      return api.messages(folder, debouncedQuery, cursor, activeMailboxId, messagePageSize)
    },
    initialPageParam: "",
    // 换关键词时先把上一份结果留着，避免整张列表塌成骨架屏。
    placeholderData: (previous) => previous,
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
    enabled:
      !!activeMailboxId &&
      canReadMail &&
      sharedSelectionReady &&
      mailView !== "scheduled" &&
      mailView !== "sendQueue" &&
      mailView !== "external" &&
      (mailView !== "label" || !!selectedLabelId),
  })
  const externalMessages = useInfiniteQuery({
    queryKey: [
      "external-messages",
      selectedExternalAccountId,
      externalFolder,
      debouncedQuery,
      messagePageSize,
    ],
    queryFn: ({ pageParam }) =>
      api.externalMessages(
        selectedExternalAccountId,
        externalFolder,
        typeof pageParam === "string" ? pageParam : "",
        debouncedQuery,
        messagePageSize
      ),
    initialPageParam: "",
    // 换关键词时先把上一份结果留着，避免整张列表塌成骨架屏。
    placeholderData: (previous) => previous,
    getNextPageParam: (lastPage) => lastPage.nextCursor || undefined,
    enabled:
      !!selectedExternalAccountId && canReadMail && mailView === "external" && externalImapEnabled,
  })
  React.useEffect(() => {
    setMessagePageIndex(0)
    setCompactSelectedIds([])
  }, [
    activeMailboxId,
    externalFolder,
    folder,
    mailFilter,
    mailView,
    messagePageSize,
    query,
    selectedExternalAccountId,
    selectedLabelId,
  ])
  const detail = useQuery({
    queryKey: ["message", selectedId, mailView, selectedExternalAccountId],
    queryFn: () =>
      mailView === "external"
        ? api.externalMessage(selectedExternalAccountId, selectedId!)
        : api.message(selectedId!, { markRead: false }),
    enabled:
      !!selectedId &&
      canReadMail &&
      (mailView !== "external" || (!!selectedExternalAccountId && externalImapEnabled)),
  })
  const thread = useQuery({
    queryKey: ["message-thread", selectedId],
    queryFn: () => api.messageThread(selectedId!),
    enabled: !!selectedId && mailView !== "external" && !!detail.data?.threadId,
  })
  const notificationItems = isPlainInboxView
    ? messages.data?.pages[0]?.items
    : inboxProbe.data?.items
  const autoRefreshing = Boolean(
    publicSettings.data?.mailAutoRefresh && (listAutoRefreshing || inboxProbe.isRefetching)
  )
  // 乐观更新的回滚快照：详情与列表都可能被同一次操作改到，出错时整体还原。
  type MessageCacheSnapshot = {
    details: [readonly unknown[], MailMessage | undefined][]
    lists: [readonly unknown[], InfiniteData<MailListResponse> | undefined][]
  }
  function snapshotMessageCaches(): MessageCacheSnapshot {
    return {
      details: qc.getQueriesData<MailMessage>({ queryKey: ["message"] }),
      lists: [
        ...qc.getQueriesData<InfiniteData<MailListResponse>>({ queryKey: ["messages"] }),
        ...qc.getQueriesData<InfiniteData<MailListResponse>>({ queryKey: ["external-messages"] }),
      ],
    }
  }
  function restoreMessageCaches(snapshot?: MessageCacheSnapshot) {
    if (!snapshot) return
    for (const [key, data] of snapshot.details) qc.setQueryData(key, data)
    for (const [key, data] of snapshot.lists) qc.setQueryData(key, data)
  }
  function updateCachedMessage(id: string, patch: Partial<MailMessage>) {
    patchCachedMessage(id, (message) => ({ ...message, ...patch }))
  }
  function patchCachedMessage(id: string, updater: (message: MailMessage) => MailMessage) {
    // 详情缓存是 ["message", id, view, externalAccountId]，setQueryData 只认精确 key，
    // 写 ["message", id] 只会造出一个没人订阅的幽灵条目（乐观更新全部失效、失败回滚也回滚不到）。
    // 这里用前缀匹配的 setQueriesData。
    qc.setQueriesData({ queryKey: ["message", id] }, (current: MailMessage | undefined) =>
      current ? updater(current) : current
    )
    qc.setQueriesData(
      { queryKey: ["messages"] },
      (current: InfiniteData<MailListResponse> | undefined) => {
        if (!current?.pages) return current
        return {
          ...current,
          pages: current.pages.map((page) => ({
            ...page,
            items: (page.items || []).map((message) =>
              message.id === id ? updater(message) : message
            ),
          })),
        }
      }
    )
    qc.setQueriesData(
      { queryKey: ["external-messages"] },
      (current: InfiniteData<MailListResponse> | undefined) => {
        if (!current?.pages) return current
        return {
          ...current,
          pages: current.pages.map((page) => ({
            ...page,
            items: (page.items || []).map((message) =>
              message.id === id ? updater(message) : message
            ),
          })),
        }
      }
    )
  }
  const star = useMutation({
    mutationFn: ({ id, starred }: { id: string; starred: boolean }) => api.star(id, starred),
    onMutate: ({ id, starred }) => {
      const snapshot = snapshotMessageCaches()
      updateCachedMessage(id, { isStarred: starred })
      return snapshot
    },
    onSuccess: async (_, variables) => {
      await qc.invalidateQueries({ queryKey: ["messages"] })
      await qc.invalidateQueries({ queryKey: ["message", variables.id] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
      await qc.invalidateQueries({ queryKey: ["labels"] })
    },
    onError: (error, _variables, snapshot) => {
      restoreMessageCaches(snapshot)
      toast({ title: "操作失败", description: errorMessage(error) })
    },
  })
  const markRead = useMutation({
    mutationFn: ({ id, read }: { id: string; read: boolean }) => api.markRead(id, read),
    onMutate: ({ id, read }) => {
      const snapshot = snapshotMessageCaches()
      updateCachedMessage(id, { isRead: read })
      return snapshot
    },
    onSuccess: async (_, variables) => {
      await qc.invalidateQueries({ queryKey: ["messages"] })
      await qc.invalidateQueries({ queryKey: ["message", variables.id] })
      await qc.invalidateQueries({ queryKey: ["folders"] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
    },
    onError: (error, _variables, snapshot) => {
      restoreMessageCaches(snapshot)
      toast({ title: "操作失败", description: errorMessage(error) })
    },
  })
  const markExternalRead = useMutation({
    mutationFn: ({ id, remoteId, read }: { id: string; remoteId: string; read: boolean }) =>
      externalMessagesStoredLocally
        ? api.markRead(remoteId, read)
        : api.markExternalRead(id, remoteId, read),
    onMutate: ({ remoteId, read }) => {
      const snapshot = snapshotMessageCaches()
      updateCachedMessage(remoteId, { isRead: read })
      return snapshot
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["external-messages"] })
      await qc.invalidateQueries({ queryKey: ["message"] })
      await qc.invalidateQueries({ queryKey: ["mail-external-folders"] })
      await qc.invalidateQueries({ queryKey: ["folders"] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
    },
    onError: (error, _variables, snapshot) => {
      restoreMessageCaches(snapshot)
      toast({ title: "操作失败", description: errorMessage(error) })
    },
  })
  const addLabel = useMutation({
    mutationFn: ({ id, label }: { id: string; label: MailLabel }) =>
      api.addLabel(id, { name: label.name, color: label.color }),
    onMutate: async ({ id, label }) => {
      await qc.cancelQueries({ queryKey: ["messages"] })
      const snapshot = snapshotMessageCaches()
      patchCachedMessage(id, (message) =>
        (message.labels || []).some((item) => item.id === label.id)
          ? message
          : { ...message, labels: [...(message.labels || []), label] }
      )
      return snapshot
    },
    onError: (_error, _vars, snapshot) => {
      restoreMessageCaches(snapshot)
      toast({ title: "添加标签失败" })
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["labels"] })
    },
  })
  const removeLabel = useMutation({
    mutationFn: ({ id, labelId }: { id: string; labelId: string }) => api.removeLabel(id, labelId),
    onMutate: async ({ id, labelId }) => {
      await qc.cancelQueries({ queryKey: ["messages"] })
      const snapshot = snapshotMessageCaches()
      patchCachedMessage(id, (message) => ({
        ...message,
        labels: (message.labels || []).filter((item) => item.id !== labelId),
      }))
      return snapshot
    },
    onError: (_error, _vars, snapshot) => {
      restoreMessageCaches(snapshot)
      toast({ title: "移除标签失败" })
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["labels"] })
    },
  })
  const createLabel = useMutation({
    mutationFn: (name: string) => api.createLabel({ mailboxId: activeMailboxId, name }),
    onMutate: async (name) => {
      await qc.cancelQueries({ queryKey: ["labels"] })
      const prevLabels = qc.getQueryData<ListResponse<MailLabel>>(["labels", activeMailboxId])
      const tempLabel: MailLabel = { id: `temp-${Date.now()}`, name, color: "" }
      qc.setQueryData<ListResponse<MailLabel>>(["labels", activeMailboxId], (current) =>
        current
          ? { ...current, items: [...(current.items || []), tempLabel] }
          : { items: [tempLabel] }
      )
      return { prevLabels }
    },
    onError: (_error, _name, context) => {
      if (context?.prevLabels) qc.setQueryData(["labels", activeMailboxId], context.prevLabels)
      toast({ title: "创建标签失败" })
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["labels"] }),
  })
  const deleteLabel = useMutation({
    mutationFn: (id: string) => api.deleteLabel(id, activeMailboxId),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: ["labels"] })
      await qc.cancelQueries({ queryKey: ["messages"] })
      if (selectedId) await qc.cancelQueries({ queryKey: ["message", selectedId] })
      const prevLabels = qc.getQueryData<ListResponse<MailLabel>>(["labels", activeMailboxId])
      const prevMessage = selectedId
        ? qc.getQueryData<MailMessage>(["message", selectedId])
        : undefined
      qc.setQueryData<ListResponse<MailLabel>>(["labels", activeMailboxId], (current) =>
        current ? { ...current, items: (current.items || []).filter((l) => l.id !== id) } : current
      )
      if (selectedId)
        qc.setQueryData<MailMessage>(["message", selectedId], (current) =>
          current
            ? { ...current, labels: (current.labels || []).filter((l) => l.id !== id) }
            : current
        )
      qc.setQueriesData<InfiniteData<MailListResponse>>({ queryKey: ["messages"] }, (current) =>
        current
          ? {
              ...current,
              pages: current.pages.map((page) => ({
                ...page,
                items: (page.items || []).map((m) => ({
                  ...m,
                  labels: (m.labels || []).filter((l) => l.id !== id),
                })),
              })),
            }
          : current
      )
      return { prevLabels, prevMessage }
    },
    onSuccess: (_data, id) => {
      if (mailView === "label" && selectedLabelId === id) {
        setMailView("folder")
        setFolder("Inbox")
        setSelectedLabelId("")
        setSelectedId(null)
      }
    },
    onError: (_error, _id, context) => {
      if (context?.prevLabels) qc.setQueryData(["labels", activeMailboxId], context.prevLabels)
      if (selectedId && context?.prevMessage)
        qc.setQueriesData(
          { queryKey: ["message", selectedId] },
          (current: MailMessage | undefined) => context.prevMessage || current
        )
      qc.invalidateQueries({ queryKey: ["messages"] })
      toast({ title: "删除标签失败" })
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["labels"] })
      qc.invalidateQueries({ queryKey: ["messages"] })
      // 详情缓存键带 mailView/externalAccountId，精确键匹配不到；
      // 用前缀失效，否则删掉的标签会一直挂在已打开邮件上。
      qc.invalidateQueries({ queryKey: ["message"] })
    },
  })
  const del = useMutation({
    mutationFn: (id: string) => api.delete(id),
    onSuccess: async () => {
      setSelectedId(null)
      setPendingConfirm(null)
      await qc.invalidateQueries({ queryKey: ["messages"] })
      await qc.invalidateQueries({ queryKey: ["folders"] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
      await qc.invalidateQueries({ queryKey: ["labels"] })
      toast({ title: "已删除" })
    },
    onError: (error) => toast({ title: "删除失败", description: errorMessage(error) }),
  })
  const move = useMutation({
    mutationFn: ({ id, folder }: { id: string; folder: string }) => api.move(id, folder),
    onSuccess: async () => {
      setSelectedId(null)
      await qc.invalidateQueries({ queryKey: ["messages"] })
      await qc.invalidateQueries({ queryKey: ["folders"] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
      await qc.invalidateQueries({ queryKey: ["labels"] })
      toast({ title: "已移动" })
    },
    onError: (error) => toast({ title: "移动失败", description: errorMessage(error) }),
  })
  const cancelScheduledSend = useMutation({
    mutationFn: (item: ScheduledSend) => api.cancelScheduledSend(item.id),
    onMutate: (item) => setCancelingScheduledId(item.id),
    onSuccess: async (_, item) => {
      await qc.invalidateQueries({ queryKey: ["scheduled-sends"] })
      toast({ title: item.status === "failed" ? "已移除失败记录" : "已取消定时发送" })
    },
    onError: (error) =>
      toast({
        title: "操作失败",
        description: errorMessage(error),
      }),
    onSettled: () => setCancelingScheduledId(""),
  })
  const createFolder = useMutation({
    mutationFn: (name: string) => api.createFolder({ mailboxId: activeMailboxId, name }),
    onSuccess: (created) => {
      qc.invalidateQueries({ queryKey: ["folders"] })
      setFolderDialogOpen(false)
      openFolder(created.name)
      toast({ title: "文件夹已创建" })
    },
    onError: (error) =>
      toast({
        title: "创建文件夹失败",
        description: errorMessage(error),
      }),
  })
  const reorderFolders = useMutation({
    mutationFn: (items: { id: string; sortOrder: number }[]) =>
      api.reorderFolders({
        mailboxId: activeMailboxId,
        folderIds: items.map((item) => item.id),
        folders: items,
      }),
    onMutate: async (items) => {
      await qc.cancelQueries({ queryKey: ["folders", activeMailboxId] })
      const previous = qc.getQueryData<ListResponse<MailFolder>>(["folders", activeMailboxId])
      qc.setQueryData<ListResponse<MailFolder>>(["folders", activeMailboxId], (current) => {
        if (!current?.items) return current
        const order = new Map(items.map((item) => [item.id, item.sortOrder]))
        return {
          ...current,
          items: current.items
            .map((item) =>
              order.has(item.id)
                ? { ...item, sortOrder: order.get(item.id) || item.sortOrder }
                : item
            )
            .sort(compareMailFolders),
        }
      })
      return { previous }
    },
    onError: (error, _items, context) => {
      if (context?.previous) qc.setQueryData(["folders", activeMailboxId], context.previous)
      toast({
        title: "文件夹排序失败",
        description: errorMessage(error),
      })
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["folders", activeMailboxId] }),
  })
  const deleteFolder = useMutation({
    mutationFn: (item: Extract<MailMenuItem, { type: "folder" }>) =>
      api.deleteFolder(item.folderId, activeMailboxId),
    onSuccess: async (result, item) => {
      setPendingConfirm(null)
      if (mailView === "folder" && folder === item.folderName) {
        setFolder("Inbox")
        setSelectedId(null)
      }
      await refreshMailData()
      toast({
        title: "文件夹已删除",
        description:
          result.moved > 0 ? uiMessage("已将 {0} 封邮件移回收件箱", [result.moved]) : undefined,
      })
    },
    onError: (error) =>
      toast({
        title: "删除文件夹失败",
        description: errorMessage(error),
      }),
  })
  const retrySendQueue = useMutation({
    mutationFn: (item: SendQueueItem) => api.retrySendQueue(item.id),
    onMutate: (item) => setSendQueuePendingId(item.id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["send-queue"] })
      await qc.invalidateQueries({ queryKey: ["send-queue-audit"] })
      toast({ title: "已重新加入发送队列" })
    },
    onError: (error) =>
      toast({
        title: "重试失败",
        description: errorMessage(error),
      }),
    onSettled: () => setSendQueuePendingId(""),
  })
  const cancelSendQueue = useMutation({
    mutationFn: (item: SendQueueItem) => api.cancelSendQueue(item.id),
    onMutate: (item) => setSendQueuePendingId(item.id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["send-queue"] })
      await qc.invalidateQueries({ queryKey: ["send-queue-audit"] })
      toast({ title: "已取消发送任务" })
    },
    onError: (error) =>
      toast({
        title: "取消失败",
        description: errorMessage(error),
      }),
    onSettled: () => setSendQueuePendingId(""),
  })
  const markAllRead = useMutation({
    mutationFn: async (items: MailMessage[]) => {
      // 文件夹视图下点「全部已读」要真的把整个文件夹标掉，而不是只标当前页；
      // 星标/标签视图没有单一文件夹语义，仍回退为逐封标记当前列表。
      if (mailView === "folder" && folder && activeMailboxId) {
        let total = 0
        let remaining = 1
        // 服务端一次最多 500 封（逐封重命名 maildir 文件），这里续调到清空；
        // 上限 100 轮 = 5 万封，兜住异常情况下无限循环。
        for (let attempt = 0; attempt < 100 && remaining > 0; attempt++) {
          const result = await api.readAllFolder(activeMailboxId, folder)
          total += result.updated
          remaining = result.remaining
        }
        return total
      }
      const unread = items.filter((message) => !message.isRead)
      await runWithConcurrency(unread, 4, (message) => api.markRead(message.id, true))
      return unread.length
    },
    onSuccess: async (count) => {
      await qc.invalidateQueries({ queryKey: ["messages"] })
      await qc.invalidateQueries({ queryKey: ["folders"] })
      await qc.invalidateQueries({ queryKey: ["mail-stats"] })
      await qc.invalidateQueries({ queryKey: ["labels"] })
      toast({
        title: count > 0 ? uiMessage("已标记 {0} 封邮件为已读", [count]) : "当前没有未读邮件",
      })
    },
    onError: (error) => toast({ title: "操作失败", description: errorMessage(error) }),
  })

  React.useEffect(() => {
    if (!mailboxList.isSuccess) return
    const items = mailboxList.data?.items || []
    if (items.length === 0) {
      if (selectedMailboxId) {
        setSelectedMailboxId("")
        setSelectedId(null)
      }
      localStorage.removeItem("lanqin:selected-mailbox")
      return
    }
    if (!selectedMailboxId || !items.some((item) => item.id === selectedMailboxId)) {
      setSelectedMailboxId(items[0].id)
    }
  }, [mailboxList.isSuccess, mailboxList.data?.items, selectedMailboxId])

  const pendingOpenIdRef = React.useRef<string | null>(null)

  React.useEffect(() => {
    if (selectedMailboxId) localStorage.setItem("lanqin:selected-mailbox", selectedMailboxId)
    else localStorage.removeItem("lanqin:selected-mailbox")
  }, [selectedMailboxId])

  React.useEffect(() => {
    // 从星标/标签/定时视图点「新邮件」通知时，openFirstMessage 会先把
    // mailView 切回 folder，这个 effect 又会把刚选中的邮件清掉，
    // 结果只切了视图、详情没打开。用一个一次性 pending 值把它带过去。
    const pending = pendingOpenIdRef.current
    pendingOpenIdRef.current = null
    if (pending) {
      setSelectedId(pending)
      return
    }
    setSelectedId(null)
    setMailFilter("all")
  }, [mailView])

  React.useEffect(() => {
    if (!deepLinkMailboxId || !deepLinkMessageId || !mailboxList.isSuccess) return
    const target = mailboxList.data?.items.find((item) => item.id === deepLinkMailboxId)
    if (!target) {
      toast({ title: "无法打开邮件", description: "邮件链接无效或当前账号无权访问" })
      const next = new URLSearchParams(urlParams)
      next.delete("mailboxId")
      next.delete("messageId")
      setURLParams(next, { replace: true })
      return
    }
    if (selectedMailboxId !== target.id) {
      setSelectedMailboxId(target.id)
      return
    }
    if (mailView !== "folder" || folder !== "Inbox") {
      setSelectedExternalAccountId("")
      setSelectedLabelId("")
      setMailView("folder")
      setFolder("Inbox")
      setMailFilter("all")
      return
    }
    setSelectedId(deepLinkMessageId)
    const next = new URLSearchParams(urlParams)
    next.delete("mailboxId")
    next.delete("messageId")
    setURLParams(next, { replace: true })
  }, [
    deepLinkMailboxId,
    deepLinkMessageId,
    folder,
    mailView,
    mailboxList.data?.items,
    mailboxList.isSuccess,
    selectedMailboxId,
    setURLParams,
    toast,
    urlParams,
  ])

  React.useEffect(() => {
    setCompactSelectedIds([])
  }, [selectedMailboxId, mailView, folder, selectedLabelId, query, displayMode])

  React.useEffect(() => {
    applyTheme(darkMode, themeMountedRef.current)
    themeMountedRef.current = true
  }, [darkMode])

  React.useEffect(() => {
    const unlock = () => {
      const AudioContextCtor =
        window.AudioContext ||
        (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
      if (AudioContextCtor && !mailAudioContextRef.current) {
        const ctx = new AudioContextCtor()
        mailAudioContextRef.current = ctx
        if (ctx.state === "suspended") void ctx.resume()
      }
      if ("Notification" in window && Notification.permission === "default") {
        void Notification.requestPermission()
      }
    }
    window.addEventListener("pointerdown", unlock, { once: true })
    window.addEventListener("keydown", unlock, { once: true })
    return () => {
      window.removeEventListener("pointerdown", unlock)
      window.removeEventListener("keydown", unlock)
    }
  }, [])

  React.useEffect(() => {
    if (!activeMailboxId || !notificationItems) return
    const items = notificationItems
    const latest = items[0]
    const nextState = { latestId: latest?.id || "", latestReceivedAt: latest?.receivedAt || "" }
    const prevState = mailNotifyStateRef.current[activeMailboxId]
    if (!prevState) {
      mailNotifyStateRef.current[activeMailboxId] = nextState
      return
    }
    if (nextState.latestReceivedAt < prevState.latestReceivedAt) return
    const newMessages = items.filter(
      (item) => item.receivedAt > prevState.latestReceivedAt && item.id !== prevState.latestId
    )
    mailNotifyStateRef.current[activeMailboxId] = nextState
    if (newMessages.length === 0) return

    void qc.invalidateQueries({ queryKey: ["folders"] })
    void qc.invalidateQueries({ queryKey: ["mail-stats"] })
    void qc.invalidateQueries({ queryKey: ["labels"] })

    const first = newMessages[0]
    const firstSender = senderDisplayName(first)
    const title =
      newMessages.length > 1
        ? uiMessage("收到 {0} 封新邮件", [newMessages.length])
        : uiMessage("新邮件：{0}", [first.subject || uiMessage("(无主题)")])
    const description =
      newMessages.length > 1
        ? uiMessage("{0} 等发来新邮件", [firstSender])
        : uiMessage("{0}", [`${firstSender}${first.snippet ? ` · ${first.snippet}` : ""}`])
    const openFirstMessage = () => {
      pendingOpenIdRef.current = first.id
      setMailView("folder")
      setFolder("Inbox")
      if (mailView === "folder") setSelectedId(first.id)
      void api.markRead(first.id, true).catch(() => undefined)
    }
    toast({
      title,
      description,
      onClick: openFirstMessage,
      onKeyDown: (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault()
          openFirstMessage()
        }
      },
      role: "button",
      tabIndex: 0,
    })
    playIncomingMailSound(mailAudioContextRef)
    if ("Notification" in window && Notification.permission === "granted") {
      const notification = new Notification(uiText(title), {
        body: uiText(description),
        tag: `lanqin-mail-${activeMailboxId}`,
      })
      notification.onclick = () => {
        window.focus()
        openFirstMessage()
        notification.close()
      }
    }
  }, [activeMailboxId, notificationItems, qc, toast])

  React.useEffect(() => {
    let source: EventSource | null = null
    let closed = false
    let attempts = 0
    let timer: number | undefined
    const connect = () => {
      source = new EventSource("/api/events", { withCredentials: true })
      source.addEventListener("sync", () => {
        qc.invalidateQueries({ queryKey: ["folders"] })
        qc.invalidateQueries({ queryKey: ["mail-stats"] })
        qc.invalidateQueries({ queryKey: ["labels"] })
        qc.invalidateQueries({ queryKey: ["mail-notifications"] })
      })
      source.onopen = () => {
        attempts = 0
      }
      source.onerror = () => {
        // 浏览器默认每 3 秒无限重连；登录过期或网关挂掉时会一直打点。
        // 这里主动关掉，按 3s→6s→12s→24s→60s 退避重建。
        source?.close()
        if (closed) return
        attempts += 1
        const delay = Math.min(60000, 3000 * 2 ** Math.min(attempts - 1, 4))
        timer = window.setTimeout(connect, delay)
      }
    }
    connect()
    return () => {
      closed = true
      window.clearTimeout(timer)
      source?.close()
    }
  }, [qc])

  React.useEffect(() => {
    if (
      !mailRefreshInterval ||
      !activeMailboxId ||
      !canReadMail ||
      mailView === "scheduled" ||
      mailView === "sendQueue"
    )
      return
    if (mailView === "label" && !selectedLabelId) return
    if (mailView === "external" && (!externalImapEnabled || !selectedExternalAccountId)) return

    let stopped = false
    let inFlight = false
    const probeFirstPage = async () => {
      if (inFlight) return
      inFlight = true
      setListAutoRefreshing(true)
      try {
        const queryKey =
          mailView === "external"
            ? [
                "external-messages",
                selectedExternalAccountId,
                externalFolder,
                debouncedQuery,
                messagePageSize,
              ]
            : [
                "messages",
                activeMailboxId,
                mailView,
                folder,
                selectedLabelId,
                debouncedQuery,
                messagePageSize,
              ]
        const nextPage =
          mailView === "external"
            ? await api.externalMessages(
                selectedExternalAccountId,
                externalFolder,
                "",
                debouncedQuery,
                messagePageSize
              )
            : mailView === "starred"
              ? await api.starredMessages(debouncedQuery, "", activeMailboxId, messagePageSize)
              : mailView === "label"
                ? await api.labelMessages(
                    selectedLabelId,
                    debouncedQuery,
                    "",
                    activeMailboxId,
                    messagePageSize
                  )
                : await api.messages(folder, debouncedQuery, "", activeMailboxId, messagePageSize)
        const current = qc.getQueryData<InfiniteData<MailListResponse>>(queryKey)
        if (current?.pages[0] && JSON.stringify(current.pages[0]) !== JSON.stringify(nextPage)) {
          await qc.invalidateQueries({ queryKey, exact: true })
        }
      } catch {
        // A transient probe failure should not replace the currently displayed mailbox data.
      } finally {
        inFlight = false
        if (!stopped) {
          window.setTimeout(() => {
            if (!stopped) setListAutoRefreshing(false)
          }, 600)
        }
      }
    }
    const timer = window.setInterval(() => void probeFirstPage(), mailRefreshInterval)
    return () => {
      stopped = true
      window.clearInterval(timer)
      setListAutoRefreshing(false)
    }
  }, [
    activeMailboxId,
    canReadMail,
    externalFolder,
    externalImapEnabled,
    folder,
    mailRefreshInterval,
    mailView,
    messagePageSize,
    qc,
    debouncedQuery,
    selectedExternalAccountId,
    selectedLabelId,
  ])

  const selected = detail.data
  const messagePages =
    (mailView === "external" ? externalMessages.data?.pages : messages.data?.pages) || []
  const currentMessagePage = messagePages[messagePageIndex] || messagePages[0]
  const allMessages = currentMessagePage?.items || []
  const messageTotalCount = currentMessagePage?.totalCount
  React.useEffect(() => {
    if (
      messagePageIndex > 0 &&
      currentMessagePage &&
      (currentMessagePage.items?.length || 0) === 0
    ) {
      setMessagePageIndex((page) => Math.max(0, page - 1))
    }
  }, [currentMessagePage, messagePageIndex])
  const visibleMessages = allMessages.filter((message) => {
    if (mailFilter === "unread") return !message.isRead
    if (mailFilter === "starred") return message.isStarred
    if (mailFilter === "attachments") return message.hasAttachments
    return true
  })
  const unreadCount = allMessages.filter((message) => !message.isRead).length
  const starredCount =
    mailStats.data?.starredMessages ?? (mailView === "starred" ? allMessages.length : 0)
  const scheduledItems = scheduledSends.data?.items || []
  const scheduledDraftIds = new Set(
    scheduledItems
      .map((item) => item.draftId)
      .filter((draftId): draftId is string => Boolean(draftId))
  )
  const scheduledCount = scheduledItems.length
  const scheduledQuery = query.trim().toLowerCase()
  const visibleScheduledItems = scheduledQuery
    ? scheduledItems.filter((item) =>
        [item.subject, item.snippet, ...(item.to || [])]
          .join(" ")
          .toLowerCase()
          .includes(scheduledQuery)
      )
    : scheduledItems
  const sendQueueItems = sendQueue.data?.items || []
  const sendQueueCount = sendQueueItems.filter(
    (item) => item.status === "failed" || item.status === "queued" || item.status === "sending"
  ).length
  const visibleSendQueueItems = sendQueueItems
  const includeStarredView =
    selectedMailboxOwned ||
    selectedMailbox?.shareScope === "all" ||
    !!selectedMailbox?.shareIncludesStarred
  const mailMenuItems = buildMailMenuItems(
    folders.data?.items || [],
    starredCount,
    includeStarredView,
    selectedMailbox?.access === "read" && selectedMailbox.shareScope === "custom",
    canScheduleSelectedMailbox ? scheduledCount : 0,
    canScheduleSelectedMailbox,
    canViewSendQueue ? sendQueueCount : 0,
    canViewSendQueue
  )
  const externalAccountItems = externalImapEnabled ? externalMailAccounts.data?.items || [] : []
  const externalFolderItems = externalImapEnabled ? externalFolders.data?.items || [] : []
  const labelItems = labels.data?.items || []
  const selectedLabel = labelItems.find((item) => item.id === selectedLabelId)
  const viewTitle =
    mailView === "external"
      ? `${selectedExternalAccount?.name || uiText("外部邮箱")} · ${folderLabels[externalFolder] ? uiText(folderLabels[externalFolder]) : externalFolder}`
      : mailView === "sendQueue"
        ? uiText("发送队列")
        : mailView === "scheduled"
          ? uiText("待发送")
          : mailView === "starred"
            ? uiText("星标邮件")
            : mailView === "label"
              ? selectedLabel?.name || uiText("标签")
              : folderLabels[folder]
                ? uiText(folderLabels[folder])
                : folder
  const emptyMessage = getEmptyMessage(
    mailView,
    mailView === "external" ? externalFolder : folder,
    allMessages.length
  )
  const visibleMessageIds = visibleMessages.map((message) => message.id)
  const selectedCountOnPage = compactSelectedIds.filter((id) =>
    visibleMessageIds.includes(id)
  ).length
  const compactAllSelected =
    visibleMessageIds.length > 0 && selectedCountOnPage === visibleMessageIds.length
  const compactSomeSelected = selectedCountOnPage > 0 && !compactAllSelected
  const hasPreviousMessagePage = messagePageIndex > 0
  const hasNextMessagePage =
    !!messagePages[messagePageIndex + 1] || !!currentMessagePage?.nextCursor
  const messagePageLoading =
    mailView === "external" ? externalMessages.isFetchingNextPage : messages.isFetchingNextPage
  function toggleCompactSelectAll(checked: boolean) {
    setCompactSelectedIds(checked ? visibleMessageIds : [])
  }
  function toggleCompactSelect(messageId: string, checked: boolean) {
    setCompactSelectedIds((ids) =>
      checked ? Array.from(new Set([...ids, messageId])) : ids.filter((id) => id !== messageId)
    )
  }
  function setMailPageSize(size: MailPageSize) {
    localStorage.setItem(MAIL_PAGE_SIZE_KEY, String(size))
    setMessagePageSize(size)
    setMessagePageIndex(0)
    setCompactSelectedIds([])
  }
  function previousMessagePage() {
    if (!hasPreviousMessagePage) return
    setMessagePageIndex((page) => Math.max(0, page - 1))
    setCompactSelectedIds([])
  }
  async function nextMessagePage() {
    if (!hasNextMessagePage || messagePageLoading) return
    const nextIndex = messagePageIndex + 1
    if (messagePages[nextIndex]) {
      setMessagePageIndex(nextIndex)
      setCompactSelectedIds([])
      return
    }
    try {
      const result =
        mailView === "external"
          ? await externalMessages.fetchNextPage()
          : await messages.fetchNextPage()
      // 必须用 result 里最新的 pages，不能用这次渲染闭包里的 messages/externalMessages：
      // 那是 await 之前的快照，页数永远差一页，导致第一次点「下一页」只拉数据不翻页，
      // 需要再点一次才生效。
      const totalPages = result.data?.pages.length || 0
      if (totalPages > nextIndex && result.data?.pages[nextIndex]) {
        setMessagePageIndex(nextIndex)
        setCompactSelectedIds([])
      }
    } catch (error) {
      toast({ title: "加载下一页失败", description: errorMessage(error) })
    }
  }
  async function refreshMailData() {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ["messages"] }),
      qc.invalidateQueries({ queryKey: ["external-messages"] }),
      qc.invalidateQueries({ queryKey: ["mail-external-folders"] }),
      qc.invalidateQueries({ queryKey: ["mail-external-accounts"] }),
      qc.invalidateQueries({ queryKey: ["folders"] }),
      qc.invalidateQueries({ queryKey: ["mail-stats"] }),
      qc.invalidateQueries({ queryKey: ["labels"] }),
      qc.invalidateQueries({ queryKey: ["scheduled-sends"] }),
      qc.invalidateQueries({ queryKey: ["send-queue"] }),
    ])
  }
  async function runBulkAction(action: BulkAction) {
    if (!canOrganizeSelectedMailbox) return
    const ids = compactSelectedIds.filter((id) => visibleMessageIds.includes(id))
    if (ids.length === 0) return
    if (action === "delete") {
      setPendingConfirm({
        title: "删除所选邮件？",
        description: uiMessage("将删除当前选中的 {0} 封邮件，此操作无法从邮件列表中恢复。", [
          ids.length,
        ]),
        confirmText: "删除邮件",
        onConfirm: () => runConfirmedBulkAction("delete", ids),
      })
      return
    }
    await runConfirmedBulkAction(action, ids)
  }
  async function runConfirmedBulkAction(action: BulkAction, ids: string[]) {
    setBulkPending(true)
    try {
      // 一次请求处理整批：以前是每封一个 HTTP，勾 30 封就是 30 个请求排队。
      const result = await api.bulkMessageAction(ids, action)
      if (selectedId && ids.includes(selectedId)) setSelectedId(null)
      setCompactSelectedIds([])
      setPendingConfirm(null)
      await refreshMailData()
      if (result.failed?.length) {
        toast({
          title: uiMessage("已处理 {0} 封，{1} 封失败，可再试一次", [
            result.updated,
            result.failed.length,
          ]),
        })
      } else {
        toast({ title: uiMessage("已处理 {0} 封邮件", [ids.length]) })
      }
    } catch (error) {
      toast({
        title: "批量操作失败",
        description: errorMessage(error),
      })
    } finally {
      setBulkPending(false)
    }
  }
  function confirmDeleteMessage(message: MailMessage) {
    setPendingConfirm({
      title: "删除这封邮件？",
      description: uiMessage("邮件“{0}”将被删除。", [message.subject || uiMessage("无主题")]),
      confirmText: "删除邮件",
      onConfirm: () => del.mutate(message.id),
    })
  }
  function openCompose(draft?: ComposeDraft) {
    if (draft?.isDraft && !canManageSelectedDrafts) return
    if (!draft?.isDraft && !canSendSelectedMailbox) return
    setComposeDraft(draft || { key: `new-${Date.now()}` })
    setComposeOpen(true)
  }
  // 列表行对象只有 snippet，没有正文；从右键菜单「回复/转发」时必须先把整封取回来，
  // 否则引用的原文只剩摘要那一百来字（用户实测：转发后正文只剩半句话）。
  async function withFullBody(message: MailMessage): Promise<MailMessage> {
    if (message.bodyText || message.bodyHtml) return message
    try {
      return message.externalAccountId
        ? await api.externalMessage(message.externalAccountId, message.id)
        : await api.message(message.id, { markRead: false })
    } catch {
      return message
    }
  }
  async function openReply(message: MailMessage) {
    if (!canSendSelectedMailbox) return
    const full = await withFullBody(message)
    openCompose({
      key: `reply-${full.id}-${Date.now()}`,
      to: full.from,
      subject: withPrefix(full.subject, "Re:"),
      ...quoteMessage(full),
    })
  }
  async function openForward(message: MailMessage) {
    if (!canSendSelectedMailbox) return
    const full = await withFullBody(message)
    openCompose({
      key: `forward-${full.id}-${Date.now()}`,
      subject: withPrefix(full.subject, "Fwd:"),
      ...quoteMessage(full, "forward"),
    })
  }
  async function openDraft(message: MailMessage) {
    if (!canManageSelectedDrafts) return
    if (scheduledDraftIds.has(message.id)) {
      toast({ title: "这封草稿已在待发送队列中", description: "请先取消定时发送，再继续编辑。" })
      openScheduled()
      return
    }
    try {
      const detail = await api.message(message.id, { markRead: false })
      openCompose({
        key: `draft-${detail.id}-${Date.now()}`,
        id: detail.id,
        mailboxId: detail.mailboxId,
        to: (detail.to || []).join(", "),
        cc: (detail.cc || []).join(", "),
        bcc: (detail.bcc || []).join(", "),
        subject: detail.subject === "(无主题)" ? "" : detail.subject,
        text: detail.bodyText || "",
        html: detail.bodyHtml || undefined,
        files: await attachmentFilesFromMessage(detail),
        isDraft: true,
      })
      setSelectedId(null)
    } catch (error) {
      toast({
        title: "打开草稿失败",
        description: errorMessage(error),
      })
    }
  }
  function switchMailbox(mailboxId: string) {
    setSelectedMailboxId(mailboxId)
    setSelectedExternalAccountId("")
    setFolder("Inbox")
    setMailView("folder")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openFolder(nextFolder: string) {
    setSelectedExternalAccountId("")
    setFolder(nextFolder)
    setMailView("folder")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openStarred() {
    setSelectedExternalAccountId("")
    setMailView("starred")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openScheduled() {
    setSelectedExternalAccountId("")
    setMailView("scheduled")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openMessageContextMenu(event: React.MouseEvent, message: MailMessage) {
    if (mailView === "external") return
    event.preventDefault()
    event.stopPropagation()
    if (message.folder !== "Drafts") setSelectedId(message.id)
    setMessageContextMenu({ message, x: event.clientX, y: event.clientY })
  }
  function closeMessageContextMenu() {
    setMessageContextMenu(null)
  }
  function openSidebarContextMenu(event: React.MouseEvent, item: MailMenuItem) {
    event.preventDefault()
    event.stopPropagation()
    setSidebarContextMenu({ item, x: event.clientX, y: event.clientY })
  }
  function closeSidebarContextMenu() {
    setSidebarContextMenu(null)
  }
  function activateSidebarItem(item: MailMenuItem) {
    if (item.type === "starred") openStarred()
    else if (item.type === "scheduled") openScheduled()
    else if (item.type === "sendQueue") openSendQueue()
    else openFolder(item.folderName)
  }
  function openExternalFolder(account: ExternalImapAccount, folderName = "INBOX") {
    setSelectedExternalAccountId(account.id)
    setExpandedExternalAccountIds([account.id])
    setExternalFolder(folderName)
    setMailView("external")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function toggleExternalAccount(account: ExternalImapAccount) {
    if (sidebarCollapsed) {
      openExternalFolder(account, "INBOX")
      return
    }
    const expanded = expandedExternalAccountIds.includes(account.id)
    if (expanded) {
      setExpandedExternalAccountIds((items) => items.filter((id) => id !== account.id))
      return
    }
    setExpandedExternalAccountIds([account.id])
    if (selectedExternalAccountId !== account.id || mailView !== "external") {
      openExternalFolder(account, "INBOX")
    }
  }
  function reorderCustomFolder(draggedId: string, target: FolderDropTarget) {
    if (!canOrganizeSelectedMailbox || reorderFolders.isPending) return
    const foldersByID = new Map((folders.data?.items || []).map((item) => [item.id, item]))
    const dragged = foldersByID.get(draggedId)
    if (!dragged || !isCustomMailFolder(dragged)) return
    const menuWithoutDragged = mailMenuItems.filter(
      (item) => !(item.type === "folder" && item.folderId === draggedId)
    )
    let insertIndex = menuWithoutDragged.length
    if (target.edge !== "end") {
      const targetIndex = menuWithoutDragged.findIndex((item) => item.key === target.key)
      if (targetIndex < 0) return
      insertIndex = target.edge === "before" ? targetIndex : targetIndex + 1
    }
    const draggedMenuItem: MailMenuItem = {
      type: "folder",
      key: dragged.id,
      folderId: dragged.id,
      folderName: dragged.name,
      label: folderLabels[dragged.name] ? uiText(folderLabels[dragged.name]) : dragged.name,
      icon: folderIcons[dragged.role] || <Inbox className="h-4 w-4" />,
      count: dragged.name === "Drafts" ? dragged.totalCount : dragged.unreadCount,
      custom: true,
      order: dragged.sortOrder,
    }
    const nextMenu = [...menuWithoutDragged]
    nextMenu.splice(insertIndex, 0, draggedMenuItem)
    const nextFolders = assignCustomFolderOrders(nextMenu)
    reorderFolders.mutate(nextFolders)
  }
  function moveSidebarFolder(item: MailMenuItem, action: "top" | "up" | "down" | "bottom") {
    if (item.type !== "folder" || !item.custom) return
    const currentIndex = mailMenuItems.findIndex((entry) => entry.key === item.key)
    if (currentIndex < 0) return
    if (action === "top") {
      reorderCustomFolder(item.folderId, {
        key: mailMenuItems[0]?.key || "__end__",
        edge: "before",
      })
      return
    }
    if (action === "bottom") {
      reorderCustomFolder(item.folderId, { key: "__end__", edge: "end" })
      return
    }
    const targetIndex = action === "up" ? currentIndex - 1 : currentIndex + 1
    const target = mailMenuItems[targetIndex]
    if (!target) return
    reorderCustomFolder(item.folderId, {
      key: target.key,
      edge: action === "up" ? "before" : "after",
    })
  }
  function confirmDeleteFolder(item: MailMenuItem) {
    if (item.type !== "folder" || !item.custom) return
    setPendingConfirm({
      title: uiMessage("删除文件夹“{0}”？", [item.label]),
      description: "文件夹内的邮件会移回收件箱，不会被删除。",
      confirmText: "删除文件夹",
      onConfirm: () => deleteFolder.mutate(item),
    })
  }
  function handleFolderDragStart(event: React.DragEvent, item: MailMenuItem) {
    if (item.type !== "folder" || !item.custom || sidebarCollapsed || !canOrganizeSelectedMailbox)
      return
    event.dataTransfer.effectAllowed = "move"
    event.dataTransfer.setData("text/plain", item.folderId)
    setDraggingFolderId(item.folderId)
  }
  function handleFolderDragOver(event: React.DragEvent, item: MailMenuItem) {
    if (!draggingFolderId || item.key === draggingFolderId) return
    event.preventDefault()
    event.dataTransfer.dropEffect = "move"
    const rect = event.currentTarget.getBoundingClientRect()
    setFolderDropTarget({
      key: item.key,
      edge: event.clientY < rect.top + rect.height / 2 ? "before" : "after",
    })
  }
  function handleFolderDrop(event: React.DragEvent, item: MailMenuItem) {
    if (!draggingFolderId) return
    event.preventDefault()
    const draggedId = event.dataTransfer.getData("text/plain") || draggingFolderId
    const rect = event.currentTarget.getBoundingClientRect()
    const edge = event.clientY < rect.top + rect.height / 2 ? "before" : "after"
    setDraggingFolderId("")
    setFolderDropTarget(null)
    reorderCustomFolder(draggedId, { key: item.key, edge })
  }
  function clearFolderDragState() {
    setDraggingFolderId("")
    setFolderDropTarget(null)
  }
  function runMessageContextAction(
    action:
      "open" | "reply" | "forward" | "read" | "star" | "archive" | "trash" | "spam" | "delete",
    message: MailMessage
  ) {
    closeMessageContextMenu()
    if (action === "open") {
      openMessage(message.id)
      return
    }
    if (action === "reply") {
      openReply(message)
      return
    }
    if (action === "forward") {
      openForward(message)
      return
    }
    if (action === "read") {
      if (mailView === "external" && selectedExternalAccountId)
        markExternalRead.mutate({
          id: selectedExternalAccountId,
          remoteId: message.id,
          read: !message.isRead,
        })
      else markRead.mutate({ id: message.id, read: !message.isRead })
      return
    }
    if (action === "star") {
      if (mailView === "external" && !externalMessagesStoredLocally) return
      star.mutate({ id: message.id, starred: !message.isStarred })
      return
    }
    if (action === "archive") {
      if (mailView === "external" && !externalMessagesStoredLocally) return
      move.mutate({ id: message.id, folder: message.folder === "Archive" ? "Inbox" : "Archive" })
      return
    }
    if (action === "trash") {
      if (mailView === "external" && !externalMessagesStoredLocally) return
      move.mutate({ id: message.id, folder: "Trash" })
      return
    }
    if (action === "spam") {
      if (mailView === "external" && !externalMessagesStoredLocally) return
      move.mutate({ id: message.id, folder: "Spam" })
      return
    }
    if (mailView === "external" && !externalMessagesStoredLocally) return
    confirmDeleteMessage(message)
  }
  function openSendQueue() {
    setSelectedExternalAccountId("")
    setMailView("sendQueue")
    setSelectedLabelId("")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openMessageSendTimeline(message: MailMessage) {
    if (!message.sendQueueId) return
    setSendQueueAuditId(message.sendQueueId)
  }
  function openLabel(labelId: string) {
    setSelectedExternalAccountId("")
    setSelectedLabelId(labelId)
    setMailView("label")
    setSelectedId(null)
    setMailFilter("all")
    setMobileSidebarOpen(false)
  }
  function openMessage(messageId: string | null) {
    if (!messageId) {
      setSelectedId(null)
      return
    }
    const message = allMessages.find((item) => item.id === messageId)
    if (mailView !== "external" && message?.folder === "Drafts" && canManageSelectedDrafts) {
      void openDraft(message)
      return
    }
    setSelectedId(messageId)
    if (message && !message.isRead && canOrganizeSelectedMailbox) {
      if (mailView === "external" && selectedExternalAccountId)
        markExternalRead.mutate({ id: selectedExternalAccountId, remoteId: message.id, read: true })
      else markRead.mutate({ id: message.id, read: true })
    }
  }
  async function refreshMail() {
    if (refreshing || autoRefreshing) return
    setRefreshing(true)
    try {
      await Promise.all([
        refreshMailData(),
        new Promise((resolve) => window.setTimeout(resolve, 500)),
      ])
    } finally {
      setRefreshing(false)
    }
  }
  async function copyCurrentMailbox() {
    if (!selectedMailbox?.address) return
    const copied = await copyToClipboard(selectedMailbox.address)
    toast({ title: copied ? "邮箱地址已复制" : "复制失败，请手动选择地址" })
  }
  function openSettings() {
    navigate("/profile")
  }
  const systemMenuItems = mailMenuItems.filter((item) => !(item.type === "folder" && item.custom))
  const customFolderItems = mailMenuItems.filter((item) => item.type === "folder" && item.custom)

  const renderMailMenuItem = (item: MailMenuItem) => (
    <SidebarMenuItem
      key={item.key}
      draggable={
        item.type === "folder" && item.custom && canOrganizeSelectedMailbox && !sidebarCollapsed
      }
      onDragStart={(event) => handleFolderDragStart(event, item)}
      onDragOver={(event) => handleFolderDragOver(event, item)}
      onDragLeave={() => {
        if (folderDropTarget?.key === item.key) setFolderDropTarget(null)
      }}
      onDrop={(event) => handleFolderDrop(event, item)}
      onDragEnd={clearFolderDragState}
      onContextMenu={(event) => openSidebarContextMenu(event, item)}
    >
      <SidebarMenuButton
        isActive={
          item.type === "starred"
            ? mailView === "starred"
            : item.type === "scheduled"
              ? mailView === "scheduled"
              : item.type === "sendQueue"
                ? mailView === "sendQueue"
                : mailView === "folder" && folder === item.folderName
        }
        className={cn(
          "h-auto px-3 py-2 text-sm",
          sidebarCollapsed && "justify-center px-0",
          item.type === "folder" &&
            item.custom &&
            canOrganizeSelectedMailbox &&
            !sidebarCollapsed &&
            "cursor-grab active:cursor-grabbing",
          item.type === "folder" &&
            item.custom &&
            draggingFolderId === item.folderId &&
            "opacity-50",
          folderDropTarget?.key === item.key && "bg-accent/60",
          folderDropTarget?.key === item.key &&
            folderDropTarget.edge === "before" &&
            "border-t-2 border-t-primary",
          folderDropTarget?.key === item.key &&
            folderDropTarget.edge === "after" &&
            "border-b-2 border-b-primary"
        )}
        onClick={() => activateSidebarItem(item)}
      >
        {item.icon}
        {!sidebarCollapsed && (
          <span>{item.type === "folder" && item.custom ? item.label : uiText(item.label)}</span>
        )}
        {!sidebarCollapsed && item.count > 0 && (
          <span className="ml-auto text-xs tabular-nums text-muted-foreground">
            {uiText("{0}", [item.count])}
          </span>
        )}
      </SidebarMenuButton>
    </SidebarMenuItem>
  )

  const sidebarContent = (
    <Sidebar collapsible="none" className="h-full w-full border-r bg-sidebar">
      <SidebarHeader className={cn("border-b py-3", sidebarCollapsed ? "px-2" : "px-3")}>
        <AccountHeader
          collapsed={sidebarCollapsed}
          name={me.data?.user.displayName || selectedMailbox?.address || "LanQin"}
          email={me.data?.user.email || selectedMailbox?.address}
          darkMode={darkMode}
          onToggleTheme={() => setDarkMode((value) => !value)}
          onSettings={openSettings}
        />
        <div className={cn("mt-2 flex gap-2", sidebarCollapsed && "justify-center")}>
          <MailboxSwitcher
            collapsed={sidebarCollapsed}
            mailboxes={mailboxList.data?.items || []}
            selectedMailbox={selectedMailbox}
            onSelect={switchMailbox}
          />
          {!sidebarCollapsed && (
            <Button
              type="button"
              variant="outline"
              size="icon"
              className="h-9 w-9 shrink-0 rounded-lg"
              onClick={copyCurrentMailbox}
              disabled={!selectedMailbox}
            >
              <Copy className="h-4 w-4" />
            </Button>
          )}
        </div>
        {canSendSelectedMailbox && (
          <Button
            className={cn("mt-2 h-10 w-full rounded-lg text-sm", sidebarCollapsed && "px-0")}
            size={sidebarCollapsed ? "icon" : "default"}
            onClick={() => openCompose()}
            disabled={!selectedMailbox}
          >
            <PencilLine className="h-4 w-4" />
            {!sidebarCollapsed && <span>{uiText("写邮件")}</span>}
          </Button>
        )}
      </SidebarHeader>
      <SidebarContent className="gap-0">
        <SidebarGroup className="p-0">
          {!sidebarCollapsed && (
            <div className="flex items-center justify-between px-2 pb-1 pt-3">
              <SidebarGroupLabel className="m-0 h-5 p-0 text-xs font-semibold uppercase text-muted-foreground">
                {uiText("邮件")}
              </SidebarGroupLabel>
            </div>
          )}
          <SidebarGroupContent>
            <SidebarMenu>{systemMenuItems.map(renderMailMenuItem)}</SidebarMenu>
            {folders.isLoading && <FolderSkeleton />}
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup className="p-0">
          {!sidebarCollapsed && (
            <div className="flex items-center justify-between px-2 pb-1 pt-3">
              <button
                type="button"
                className="flex items-center gap-1 text-xs font-semibold uppercase text-muted-foreground transition-colors hover:text-foreground"
                aria-expanded={!foldersCollapsed}
                onClick={() => setFoldersCollapsed((value) => !value)}
              >
                <ChevronDown
                  className={cn(
                    "h-3.5 w-3.5 transition-transform",
                    foldersCollapsed && "-rotate-90"
                  )}
                />
                <SidebarGroupLabel className="m-0 h-5 p-0 text-xs font-semibold uppercase text-muted-foreground">
                  {uiText("文件夹")}
                </SidebarGroupLabel>
              </button>
              {canOrganizeSelectedMailbox && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5"
                  onClick={() => setFolderDialogOpen(true)}
                  disabled={!activeMailboxId}
                >
                  <Plus className="h-3.5 w-3.5" />
                </Button>
              )}
            </div>
          )}
          <SidebarGroupContent>
            {!foldersCollapsed && (
              <SidebarMenu>
                {customFolderItems.map(renderMailMenuItem)}
                {!folders.isLoading && customFolderItems.length === 0 && (
                  <div className="py-2 pl-8 pr-3 text-sm text-muted-foreground">
                    {uiText("暂无自定义文件夹")}
                  </div>
                )}
              </SidebarMenu>
            )}
          </SidebarGroupContent>
        </SidebarGroup>
        {externalAccountItems.length > 0 && (
          <SidebarGroup className="p-0">
            {!sidebarCollapsed && <SidebarGroupLabel>{uiText("外部邮箱")}</SidebarGroupLabel>}
            <SidebarGroupContent>
              <SidebarMenu>
                {externalAccountItems.map((account) => {
                  const expanded = expandedExternalAccountIds.includes(account.id)
                  return (
                    <React.Fragment key={account.id}>
                      <SidebarMenuItem>
                        <SidebarMenuButton
                          isActive={
                            mailView === "external" && selectedExternalAccountId === account.id
                          }
                          size={sidebarCollapsed ? "default" : "lg"}
                          className={cn(sidebarCollapsed && "justify-center px-0")}
                          title={`${externalAccountLabel(account)}${account.name && account.name !== externalAccountLabel(account) ? ` · ${account.name}` : ""}`}
                          onClick={() => toggleExternalAccount(account)}
                        >
                          <Mail className="h-4 w-4" />
                          {!sidebarCollapsed && (
                            <>
                              <span className="min-w-0 flex-1">
                                <span className="block truncate">
                                  {externalAccountLabel(account)}
                                </span>
                                <span className="block truncate text-xs font-normal text-muted-foreground">
                                  {externalAccountSubtitle(account)}
                                </span>
                              </span>
                              <ChevronDown
                                className={cn(
                                  "h-4 w-4 text-muted-foreground transition-transform",
                                  expanded && "rotate-180"
                                )}
                              />
                            </>
                          )}
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                      {!sidebarCollapsed &&
                        expanded &&
                        externalFolderItems.map((item) => (
                          <SidebarMenuItem key={`${account.id}-${item.name}`}>
                            <SidebarMenuButton
                              isActive={externalFolder === item.name}
                              className="pl-8"
                              onClick={() => openExternalFolder(account, item.name)}
                            >
                              {folderIcons[item.role.toLowerCase()] || (
                                <Inbox className="h-4 w-4" />
                              )}
                              <span>
                                {folderLabels[item.role] || folderLabels[item.name]
                                  ? uiText(folderLabels[item.role] || folderLabels[item.name])
                                  : item.name}
                              </span>
                              {item.unreadCount > 0 && (
                                <Badge variant="secondary" className="ml-auto">
                                  {uiText("{0}", [item.unreadCount])}
                                </Badge>
                              )}
                            </SidebarMenuButton>
                          </SidebarMenuItem>
                        ))}
                      {!sidebarCollapsed && expanded && externalFolders.isLoading && (
                        <FolderSkeleton />
                      )}
                    </React.Fragment>
                  )
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        {canReadMail && (
          <SidebarGroup className="p-0">
            {!sidebarCollapsed && (
              <div className="flex items-center justify-between px-2 pb-1 pt-3">
                <button
                  type="button"
                  className="flex items-center gap-1 text-xs font-semibold uppercase text-muted-foreground transition-colors hover:text-foreground"
                  aria-expanded={!labelsCollapsed}
                  onClick={() => setLabelsCollapsed((value) => !value)}
                >
                  <ChevronDown
                    className={cn(
                      "h-3.5 w-3.5 transition-transform",
                      labelsCollapsed && "-rotate-90"
                    )}
                  />
                  <SidebarGroupLabel className="m-0 h-5 p-0 text-xs font-semibold uppercase text-muted-foreground">
                    {uiText("标签")}
                  </SidebarGroupLabel>
                </button>
                {canManageSelectedLabels && (
                  <div className="flex items-center gap-0.5">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-5 w-5"
                      onClick={() => {
                        setNewLabelEditing(true)
                        setLabelEditMode(true)
                      }}
                    >
                      <Plus className="h-3.5 w-3.5" />
                    </Button>
                    {labelItems.length > 0 && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-5 w-5"
                        onClick={() => setLabelEditMode((v) => !v)}
                      >
                        {labelEditMode ? (
                          <Check className="h-3 w-3" />
                        ) : (
                          <Pencil className="h-3 w-3" />
                        )}
                      </Button>
                    )}
                  </div>
                )}
              </div>
            )}
            <SidebarGroupContent>
              <SidebarMenu>
                {canReadMail &&
                  !labelsCollapsed &&
                  labelItems.map((label) => {
                    const colors = { backgroundColor: labelDotColor(label) }
                    return (
                      <SidebarMenuItem key={label.id}>
                        <SidebarMenuButton
                          isActive={
                            !labelEditMode && mailView === "label" && selectedLabelId === label.id
                          }
                          className={cn(sidebarCollapsed && "justify-center px-0")}
                          onClick={() => {
                            if (!labelEditMode) openLabel(label.id)
                          }}
                        >
                          <span className="flex w-4 shrink-0 items-center justify-center">
                            <span
                              className="h-3 w-3 rounded-full"
                              style={{ backgroundColor: colors.backgroundColor }}
                            />
                          </span>
                          {!sidebarCollapsed && (
                            <span className="min-w-0 flex-1 truncate text-sm">{label.name}</span>
                          )}
                          {!sidebarCollapsed && labelEditMode && canManageSelectedLabels && (
                            <button
                              type="button"
                              className="ml-auto flex h-5 w-5 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                              onClick={(e) => {
                                e.stopPropagation()
                                deleteLabel.mutate(label.id)
                              }}
                              disabled={deleteLabel.isPending}
                              aria-label={uiText("删除标签 {0}", [label.name])}
                            >
                              <Trash2 className="h-3.5 w-3.5" />
                            </button>
                          )}
                        </SidebarMenuButton>
                      </SidebarMenuItem>
                    )
                  })}
                {canReadMail &&
                  !labelsCollapsed &&
                  !sidebarCollapsed &&
                  !labels.isLoading &&
                  labelItems.length === 0 && (
                    <div className="px-2 py-1 text-xs text-muted-foreground">
                      {uiText("暂无标签")}
                    </div>
                  )}
                {canManageSelectedLabels && labelEditMode && newLabelEditing && (
                  <SidebarMenuItem>
                    <NewLabelButton
                      collapsed={sidebarCollapsed}
                      pending={createLabel.isPending}
                      onCreate={(name) => {
                        createLabel.mutate(name)
                        setNewLabelEditing(false)
                      }}
                      editing={newLabelEditing}
                      onEditingChange={setNewLabelEditing}
                    />
                  </SidebarMenuItem>
                )}
              </SidebarMenu>
              {labels.isLoading && <FolderSkeleton />}
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>
      {!isMobile && (
        <div
          className={cn(
            "mt-auto border-t p-2",
            sidebarCollapsed ? "flex flex-col items-center gap-1" : ""
          )}
        >
          <Button
            type="button"
            variant="ghost"
            size={sidebarCollapsed ? "icon" : "sm"}
            className={cn(!sidebarCollapsed && "w-full justify-start")}
            onClick={toggleSidebar}
          >
            {sidebarCollapsed ? (
              <PanelLeftOpen className="h-4 w-4" />
            ) : (
              <PanelLeftClose className="h-4 w-4" />
            )}
            {!sidebarCollapsed && <span>{uiText("收起侧栏")}</span>}
          </Button>
        </div>
      )}
    </Sidebar>
  )
  function toggleSidebar() {
    if (sidebarCollapsed) {
      sidebarPanelRef.current?.expand(12)
      setSidebarCollapsed(false)
    } else {
      sidebarPanelRef.current?.collapse()
      setSidebarCollapsed(true)
    }
  }

  const contentView = !canAccessMail ? (
    <PermissionEmptyState
      title={uiText("无邮箱前台权限")}
      description={uiText("当前账号未开启邮箱前台访问权限。")}
      onOpenSettings={openSettings}
    />
  ) : !canReadMail ? (
    <PermissionEmptyState
      title={uiText("无邮件查看权限")}
      description={uiText("当前账号可以访问邮箱前台，但未开启邮件查看权限。")}
      onOpenSettings={openSettings}
    />
  ) : !mailboxList.isLoading && !hasMailboxes ? (
    <NoMailboxState onOpenSettings={openSettings} />
  ) : mailView === "scheduled" && canScheduleSelectedMailbox ? (
    <ScheduledSendView
      compact={isMobile || displayMode === "compact"}
      items={visibleScheduledItems}
      total={scheduledItems.length}
      loading={scheduledSends.isLoading}
      query={query}
      cancelingId={cancelingScheduledId}
      onCancel={(item) => cancelScheduledSend.mutate(item)}
    />
  ) : mailView === "scheduled" ? (
    <PermissionEmptyState
      title={uiText("无定时发送权限")}
      description={uiText("当前账号不能查看或管理定时发送任务。")}
      onOpenSettings={openSettings}
    />
  ) : mailView === "sendQueue" && canViewSendQueue ? (
    <SendQueueView
      mobile={isMobile}
      dense={displayMode === "compact"}
      items={visibleSendQueueItems}
      total={sendQueueItems.length}
      loading={sendQueue.isLoading}
      status={sendQueueStatus}
      messageId={sendQueueMessageId}
      recipient={sendQueueRecipient}
      from={sendQueueFrom}
      to={sendQueueTo}
      pendingId={sendQueuePendingId}
      onStatusChange={setSendQueueStatus}
      onMessageIdChange={setSendQueueMessageId}
      onRecipientChange={setSendQueueRecipient}
      onFromChange={setSendQueueFrom}
      onToChange={setSendQueueTo}
      onClearFilters={() => {
        setSendQueueMessageId("")
        setSendQueueRecipient("")
        setSendQueueFrom("")
        setSendQueueTo("")
        setSendQueueStatus("all")
      }}
      onRetry={(item) => retrySendQueue.mutate(item)}
      onCancel={(item) => cancelSendQueue.mutate(item)}
      onAudit={(item) => setSendQueueAuditId(item.id)}
      canMutate={canSendSelectedMailbox}
    />
  ) : mailView === "sendQueue" ? (
    <PermissionEmptyState
      title={uiText("无发送队列权限")}
      description={uiText("当前账号不能查看发送队列。")}
      onOpenSettings={openSettings}
    />
  ) : isMobile || displayMode === "compact" ? (
    <CompactMailView
      title={viewTitle}
      icon={
        mailView === "label" && selectedLabel ? (
          <Badge variant="outline" className="gap-1.5 rounded-lg font-normal">
            <span
              className="h-2 w-2 shrink-0 rounded-full"
              style={{ backgroundColor: labelDotColor(selectedLabel) }}
            />
            {selectedLabel.name}
          </Badge>
        ) : undefined
      }
      messages={visibleMessages}
      pageItemCount={allMessages.length}
      total={messageTotalCount}
      selectedIds={compactSelectedIds}
      allSelected={compactAllSelected}
      someSelected={compactSomeSelected}
      loading={mailView === "external" ? externalMessages.isLoading : messages.isLoading}
      pageIndex={messagePageIndex}
      pageSize={messagePageSize}
      hasPreviousPage={hasPreviousMessagePage}
      hasNextPage={hasNextMessagePage}
      pageLoading={messagePageLoading}
      onPageSizeChange={setMailPageSize}
      onPreviousPage={previousMessagePage}
      onNextPage={() => void nextMessagePage()}
      emptyMessage={uiText(emptyMessage)}
      selectedId={selectedId}
      selected={selected}
      detailLoading={detail.isLoading}
      threadMessages={thread.data?.items || []}
      labels={labelItems}
      labelPending={addLabel.isPending || removeLabel.isPending}
      onSelect={openMessage}
      onSelectAll={toggleCompactSelectAll}
      onToggleSelected={toggleCompactSelect}
      scheduledDraftIds={scheduledDraftIds}
      onCloseReader={closeReader}
      onStar={(message) => {
        if (mailView !== "external" || externalMessagesStoredLocally)
          star.mutate({ id: message.id, starred: !message.isStarred })
      }}
      onReply={openReply}
      onForward={openForward}
      onSendTimeline={openMessageSendTimeline}
      onArchive={(message) => {
        if (mailView !== "external" || externalMessagesStoredLocally)
          move.mutate({
            id: message.id,
            folder: message.folder === "Archive" ? "Inbox" : "Archive",
          })
      }}
      onDelete={(message) => {
        if (mailView !== "external" || externalMessagesStoredLocally) confirmDeleteMessage(message)
      }}
      onToggleRead={(message) =>
        mailView === "external" && selectedExternalAccountId
          ? markExternalRead.mutate({
              id: selectedExternalAccountId,
              remoteId: message.id,
              read: !message.isRead,
            })
          : markRead.mutate({ id: message.id, read: !message.isRead })
      }
      onAddLabel={(message, label) => addLabel.mutate({ id: message.id, label })}
      onRemoveLabel={(message, labelId) => removeLabel.mutate({ id: message.id, labelId })}
      bulkPending={bulkPending}
      onBulkAction={runBulkAction}
      onContextMenu={openMessageContextMenu}
      canSend={canSendSelectedMailbox}
      canOrganize={
        canOrganizeSelectedMailbox && (mailView !== "external" || externalMessagesStoredLocally)
      }
      canManageLabels={canManageSelectedLabels && mailView !== "external"}
      canDownloadAttachments={canDownloadAttachments}
      language={language}
    />
  ) : (
    <ResizablePanelGroup direction="horizontal" className="min-h-0 flex-1">
      <ResizablePanel defaultSize={28} minSize={22} maxSize={38}>
        <div className="flex h-full min-h-0 flex-col">
          <div className="relative z-20 shrink-0 px-4 pb-3 pt-3">
            <div className="flex items-center gap-2">
              <div className="relative min-w-0 flex-1">
                <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder={uiText("搜索发件人、主题、内容...")}
                  className="h-9 py-2 pl-9 pr-8 text-sm"
                />
              </div>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    className="relative inline-flex h-9 flex-none items-center gap-1.5 rounded-lg border border-border px-2.5 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                  >
                    <SlidersHorizontal className="h-4 w-4" />
                    <span className="hidden sm:inline">{uiText("筛选")}</span>
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  {(Object.keys(filterLabels) as MailFilter[]).map((value) => (
                    <DropdownMenuItem key={value} onSelect={() => setMailFilter(value)}>
                      {uiText(filterLabels[value])}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
          <div className="shrink-0 border-b border-border p-3 md:p-4">
            <div className="flex items-center justify-between gap-2">
              <div className="flex min-w-0 flex-1 items-center gap-2">
                {canOrganizeSelectedMailbox && (
                  <Checkbox
                    aria-label={uiText("选择当前页邮件")}
                    checked={
                      compactAllSelected ? true : compactSomeSelected ? "indeterminate" : false
                    }
                    onCheckedChange={(value) => toggleCompactSelectAll(value === true)}
                    className="h-4 w-4 flex-none"
                  />
                )}
                <h1
                  className="min-w-0 flex-1 truncate whitespace-nowrap text-lg font-semibold md:text-xl"
                  title={mailView === "label" && selectedLabel ? selectedLabel.name : viewTitle}
                >
                  {mailView === "label" && selectedLabel ? selectedLabel.name : viewTitle}
                </h1>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8 flex-none text-muted-foreground"
                  onClick={refreshMail}
                  disabled={refreshing || autoRefreshing}
                  title={autoRefreshing ? uiText("自动刷新中") : uiText("刷新邮件")}
                >
                  <RefreshCcw
                    className={cn("h-4 w-4", (refreshing || autoRefreshing) && "animate-spin")}
                  />
                </Button>
                {selectedCountOnPage > 0 && canOrganizeSelectedMailbox && (
                  <BulkActionMenu pending={bulkPending} onAction={runBulkAction} />
                )}
                {canOrganizeSelectedMailbox && (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 flex-none text-muted-foreground"
                    onClick={() => markAllRead.mutate(allMessages)}
                    disabled={
              !activeMailboxId ||
              markAllRead.isPending ||
              (mailView !== "folder" && unreadCount === 0)
            }
                    title={uiText("全部已读")}
                  >
                    <MailCheck className="h-4 w-4" />
                  </Button>
                )}
              </div>
              <div className="flex flex-none overflow-hidden rounded-lg border border-border">
                <button
                  type="button"
                  onClick={() => setMailFilter("all")}
                  className={cn(
                    "whitespace-nowrap px-3 py-1 text-sm transition-colors",
                    mailFilter === "all"
                      ? "bg-accent text-foreground"
                      : "text-muted-foreground hover:bg-accent"
                  )}
                >
                  {uiText("全部")}
                </button>
                <button
                  type="button"
                  onClick={() => setMailFilter("unread")}
                  className={cn(
                    "whitespace-nowrap border-l border-border px-3 py-1 text-sm transition-colors",
                    mailFilter === "unread"
                      ? "bg-accent text-foreground"
                      : "text-muted-foreground hover:bg-accent"
                  )}
                >
                  {uiText("未读")}
                </button>
              </div>
            </div>
          </div>
          <ScrollArea key={`mail-page-${messagePageIndex}`} className="min-h-0 flex-1">
            {(mailView === "external" ? externalMessages.isLoading : messages.isLoading) && (
              <MessageSkeleton />
            )}
            {visibleMessages.map((m) => (
              <MessageRow
                key={m.id}
                message={m}
                active={selectedId === m.id}
                checked={compactSelectedIds.includes(m.id)}
                scheduled={scheduledDraftIds.has(m.id)}
                onCheckedChange={(checked) => toggleCompactSelect(m.id, checked)}
                onClick={() => openMessage(m.id)}
                onContextMenu={(event) => openMessageContextMenu(event, m)}
                onStar={() => {
                  if (mailView !== "external" || externalMessagesStoredLocally)
                    star.mutate({ id: m.id, starred: !m.isStarred })
                }}
                canOrganize={
                  canOrganizeSelectedMailbox &&
                  (mailView !== "external" || externalMessagesStoredLocally)
                }
              />
            ))}
            {!(mailView === "external" ? externalMessages.isLoading : messages.isLoading) &&
              visibleMessages.length === 0 && (
                <div className="p-8 text-center text-sm text-muted-foreground">
                  {uiText(emptyMessage)}
                </div>
              )}
          </ScrollArea>
          <MailPagination
            pageIndex={messagePageIndex}
            pageSize={messagePageSize}
            itemCount={allMessages.length}
            totalCount={messageTotalCount}
            hasPreviousPage={hasPreviousMessagePage}
            hasNextPage={hasNextMessagePage}
            loading={messagePageLoading}
            onPageSizeChange={setMailPageSize}
            onPreviousPage={previousMessagePage}
            onNextPage={() => void nextMessagePage()}
          />
        </div>
      </ResizablePanel>
      <ResizableHandle />

      <ResizablePanel defaultSize={68} minSize={44}>
        <section className="h-full min-h-0">
          {!selectedId && (
            <div className="flex h-full items-center justify-center text-muted-foreground">
              <div className="text-center">
                <Mail className="mx-auto mb-3 h-12 w-12 text-muted-foreground/40" />
                <p className="text-sm">{uiText("选择一封邮件以查看详情")}</p>
              </div>
            </div>
          )}
          {detail.isLoading && (
            <div className="space-y-4 p-6">
              <Skeleton className="h-8 w-2/3" />
              <Skeleton className="h-4 w-1/3" />
              <Separator />
              <Skeleton className="h-40 w-full" />
            </div>
          )}
          {selected && (
            <div className="flex h-full min-h-0 flex-col">
              <div className="border-b p-5">
                <div className="mb-4 flex items-center justify-between gap-3">
                  <h2 className="text-xl font-semibold">{selected.subject}</h2>
                  <div className="flex flex-wrap justify-end gap-2">
                    {canSendSelectedMailbox && (
                      <Button variant="outline" size="sm" onClick={() => openReply(selected)}>
                        <Reply className="h-4 w-4" />
                        {uiText("回复")}
                      </Button>
                    )}
                    {canSendSelectedMailbox && (
                      <Button variant="outline" size="sm" onClick={() => openForward(selected)}>
                        <Forward className="h-4 w-4" />
                        {uiText("转发")}
                      </Button>
                    )}
                    {mailView !== "external" && selected.sendQueueId && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => openMessageSendTimeline(selected)}
                      >
                        <History className="h-4 w-4" />
                        {uiText("投递时间线")}
                      </Button>
                    )}
                    {(mailView !== "external" || externalMessagesStoredLocally) &&
                      canOrganizeSelectedMailbox &&
                      (selected.folder === "Archive" ? (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => move.mutate({ id: selected.id, folder: "Inbox" })}
                        >
                          {uiText("取消归档")}
                        </Button>
                      ) : (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => move.mutate({ id: selected.id, folder: "Archive" })}
                        >
                          {uiText("归档")}
                        </Button>
                      ))}
                    {(mailView !== "external" || externalMessagesStoredLocally) &&
                      canOrganizeSelectedMailbox && (
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => confirmDeleteMessage(selected)}
                        >
                          {uiText("删除")}
                        </Button>
                      )}
                  </div>
                </div>
                <MessageMetaPanel
                  message={selected}
                  {...(canManageSelectedLabels
                    ? {
                        availableLabels: labelItems,
                        onAddLabel: (label: MailLabel) =>
                          addLabel.mutate({ id: selected.id, label }),
                        onRemoveLabel: (labelId: string) =>
                          removeLabel.mutate({ id: selected.id, labelId }),
                        labelPending: addLabel.isPending || removeLabel.isPending,
                      }
                    : {})}
                />
              </div>
              <ScrollArea className="min-h-0 flex-1">
                <div className="p-6">
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
              </ScrollArea>
            </div>
          )}
        </section>
      </ResizablePanel>
    </ResizablePanelGroup>
  )

  return (
    <div className="h-svh overflow-hidden bg-background">
      <SidebarProvider className="h-full min-h-0 w-full flex-col">
        {isMobile ? (
          <div className="flex h-full min-h-0 flex-col">
            {!selectedId && (
              <header className="flex min-h-14 shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
                <Sheet open={mobileSidebarOpen} onOpenChange={setMobileSidebarOpen}>
                  <SheetTrigger asChild>
                    <Button size="icon" variant="ghost" aria-label={uiText("打开导航")}>
                      <PanelLeftOpen className="h-4 w-4" />
                    </Button>
                  </SheetTrigger>
                  <SheetContent
                    side="left"
                    className="w-[86vw] max-w-80 p-0 [&>button]:hidden"
                    aria-describedby={undefined}
                  >
                    <SheetTitle className="sr-only">{uiText("邮箱导航")}</SheetTitle>
                    <div className="h-svh">{sidebarContent}</div>
                  </SheetContent>
                </Sheet>
                <Button
                  size="icon"
                  variant="ghost"
                  onClick={refreshMail}
                  disabled={refreshing || autoRefreshing}
                  className={cn(
                    "transition-all",
                    (refreshing || autoRefreshing) && "bg-primary/5 text-primary"
                  )}
                  title={autoRefreshing ? uiText("自动刷新中") : uiText("刷新邮件")}
                >
                  <RefreshCcw
                    className={cn("h-4 w-4", (refreshing || autoRefreshing) && "animate-spin")}
                  />
                </Button>
                <div className="min-w-0 flex-1 truncate text-lg font-semibold">
                  {mailView === "label" && selectedLabel ? (
                    <Badge variant="outline" className="gap-1.5 rounded-lg font-normal">
                      <span
                        className="h-2 w-2 shrink-0 rounded-full"
                        style={{
                          backgroundColor: labelDotColor(selectedLabel),
                        }}
                      />
                      {selectedLabel.name}
                    </Badge>
                  ) : (
                    viewTitle
                  )}
                </div>
                {canSendSelectedMailbox && (
                  <Button
                    type="button"
                    size="icon"
                    onClick={() => openCompose()}
                    disabled={!selectedMailbox}
                    aria-label={uiText("写邮件")}
                  >
                    <PencilLine className="h-4 w-4" />
                  </Button>
                )}
                {mailView !== "sendQueue" && (
                  <div className="relative basis-full">
                    <Search className="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
                    <Input
                      value={query}
                      onChange={(e) => setQuery(e.target.value)}
                      placeholder={
                        mailView === "external"
                          ? uiText("搜索远端邮件")
                          : mailView === "scheduled"
                            ? uiText("搜索待发送")
                            : uiText("搜索邮件")
                      }
                      className="h-10 pl-9"
                    />
                  </div>
                )}
              </header>
            )}
            <section className="flex min-h-0 flex-1 flex-col">{contentView}</section>
          </div>
        ) : (
          <ResizablePanelGroup direction="horizontal" className="h-full min-h-0 w-full">
            <ResizablePanel
              ref={sidebarPanelRef}
              collapsible
              collapsedSize={4}
              defaultSize={12}
              minSize={10}
              maxSize={18}
              onCollapse={() => setSidebarCollapsed(true)}
              onExpand={() => setSidebarCollapsed(false)}
            >
              {sidebarContent}
            </ResizablePanel>
            <ResizableHandle />
            <ResizablePanel defaultSize={85} minSize={60}>
              <section className="flex h-full min-h-0 flex-col">{contentView}</section>
            </ResizablePanel>
          </ResizablePanelGroup>
        )}
      </SidebarProvider>

      {(composeOpen || composeDraft) && (
        <React.Suspense fallback={null}>
          <ComposeDialog
            mailbox={selectedMailbox}
            open={composeOpen}
            draft={composeDraft}
            limits={user?.limits}
            canSend={canSendSelectedMailbox}
            canManageDrafts={canManageSelectedDrafts}
            canSchedule={canScheduleSelectedMailbox}
            canManageSignatures={canManageSignatures && selectedMailboxOwned}
            onOpenChange={(open) => {
              setComposeOpen(open)
              if (!open) setComposeDraft(undefined)
            }}
            onSent={() => {
              setComposeOpen(false)
              setComposeDraft(undefined)
              qc.invalidateQueries({ queryKey: ["messages"] })
              qc.invalidateQueries({ queryKey: ["folders"] })
              qc.invalidateQueries({ queryKey: ["mail-stats"] })
              qc.invalidateQueries({ queryKey: ["labels"] })
              qc.invalidateQueries({ queryKey: ["scheduled-sends"] })
              qc.invalidateQueries({ queryKey: ["send-queue"] })
            }}
          />
        </React.Suspense>
      )}
      <SendQueueAuditDialog
        open={!!sendQueueAuditId}
        loading={sendQueueAudit.isLoading}
        events={sendQueueAudit.data?.items || []}
        onOpenChange={(open) => {
          if (!open) setSendQueueAuditId("")
        }}
      />
      <MessageContextMenu
        state={messageContextMenu}
        labels={labelItems}
        folders={folders.data?.items || []}
        canManageBlocked={canManageBlocked}
        canSend={canSendSelectedMailbox}
        canOrganize={canOrganizeSelectedMailbox}
        canManageLabels={canManageSelectedLabels}
        labelPending={addLabel.isPending || removeLabel.isPending}
        onClose={closeMessageContextMenu}
        onAction={runMessageContextAction}
        onMoveToFolder={(message, folderName) => {
          closeMessageContextMenu()
          move.mutate({ id: message.id, folder: folderName })
        }}
        onToggleLabel={(message, label) => {
          const active = (message.labels || []).some((item) => item.id === label.id)
          if (active) removeLabel.mutate({ id: message.id, labelId: label.id })
          else addLabel.mutate({ id: message.id, label })
        }}
        onAllowSender={(message) => {
          const email = senderAddress(message)
          if (!email) {
            toast({ title: uiText("无法识别发件人地址") })
            return
          }
          void api
            .createAllowedSender({
              mailboxId: activeMailboxId || selectedMailbox?.id || "",
              email,
              reason: "",
            })
            .then(() => {
              toast({ title: uiText("已加入白名单"), description: email })
              void qc.invalidateQueries({ queryKey: ["allowed-senders"] })
            })
            .catch((error) =>
              toast({ title: uiText("加入白名单失败"), description: errorMessage(error) })
            )
        }}
      />
      <SidebarContextMenu
        state={sidebarContextMenu}
        canOrganize={canOrganizeSelectedMailbox}
        pending={reorderFolders.isPending}
        onClose={closeSidebarContextMenu}
        onOpen={(item) => {
          closeSidebarContextMenu()
          activateSidebarItem(item)
        }}
        onRefresh={() => {
          closeSidebarContextMenu()
          void refreshMail()
        }}
        onCreateFolder={() => {
          closeSidebarContextMenu()
          setFolderDialogOpen(true)
        }}
        onMove={(item, action) => {
          closeSidebarContextMenu()
          moveSidebarFolder(item, action)
        }}
        onDelete={(item) => {
          closeSidebarContextMenu()
          confirmDeleteFolder(item)
        }}
      />
      <CreateFolderDialog
        open={folderDialogOpen}
        pending={createFolder.isPending}
        onOpenChange={setFolderDialogOpen}
        onCreate={(name) => createFolder.mutate(name)}
      />
      <ConfirmDialog
        open={!!pendingConfirm}
        title={pendingConfirm?.title || ""}
        description={pendingConfirm?.description}
        confirmText={pendingConfirm?.confirmText || uiText("确认")}
        destructive
        pending={del.isPending || bulkPending}
        onOpenChange={(open) => {
          if (!open) setPendingConfirm(null)
        }}
        onConfirm={() => pendingConfirm?.onConfirm()}
      />
    </div>
  )
}

function buildMailMenuItems(
  folders: MailFolder[],
  starredCount: number,
  includeStarred: boolean,
  restrictedFolders: boolean,
  scheduledCount: number,
  includeScheduled: boolean,
  sendQueueCount: number,
  includeSendQueue: boolean
): MailMenuItem[] {
  const byName = new Map(folders.map((item) => [item.name, item]))
  const normalizedFolders = restrictedFolders
    ? [...folders]
    : ["Inbox", "Drafts", "Sent", "Archive", "Spam", "Trash"].map(
        (name) =>
          byName.get(name) || {
            id: `virtual-${name}`,
            name,
            role: name.toLowerCase(),
            sortOrder: 0,
            unreadCount: 0,
            totalCount: 0,
            uidValidity: 0,
            uidNext: 1,
            highestModseq: 1,
          }
      )
  for (const item of folders) {
    if (!normalizedFolders.some((folder) => folder.name === item.name)) normalizedFolders.push(item)
  }
  const folderItems: MailMenuItem[] = normalizedFolders.map((item) => ({
    type: "folder",
    key: item.id,
    folderId: item.id,
    folderName: item.name,
    label: folderLabels[item.name] ? uiText(folderLabels[item.name]) : item.name,
    icon: folderIcons[item.role] || <Inbox className="h-4 w-4" />,
    count: item.name === "Drafts" ? item.totalCount : item.unreadCount,
    custom: isCustomMailFolder(item),
    order: isCustomMailFolder(item) ? item.sortOrder || 100000 : menuAnchorOrder(item.name),
  }))
  const starredItem: MailMenuItem = {
    type: "starred",
    key: "starred",
    label: "星标邮件",
    icon: <Star className="h-4 w-4" />,
    count: starredCount,
    order: 2000,
  }
  const scheduledItem: MailMenuItem = {
    type: "scheduled",
    key: "scheduled",
    label: "稍后提醒",
    icon: <Clock3 className="h-4 w-4" />,
    count: scheduledCount,
    order: 6000,
  }
  const sendQueueItem: MailMenuItem = {
    type: "sendQueue",
    key: "send-queue",
    label: "发送队列",
    icon: <History className="h-4 w-4" />,
    count: sendQueueCount,
    order: 6500,
  }
  const specialItems: MailMenuItem[] = includeStarred ? [starredItem] : []
  if (includeScheduled) specialItems.push(scheduledItem)
  if (includeSendQueue) specialItems.push(sendQueueItem)
  return [...folderItems, ...specialItems].sort(
    (a, b) => a.order - b.order || a.label.localeCompare(b.label)
  )
}

function isCustomMailFolder(folder: Pick<MailFolder, "name" | "id">) {
  return (
    !folder.id.startsWith("virtual-") &&
    !["inbox", "sent", "drafts", "archive", "spam", "trash"].includes(
      folder.name.trim().toLowerCase()
    )
  )
}

function compareMailFolders(a: MailFolder, b: MailFolder) {
  return (
    (isCustomMailFolder(a) ? a.sortOrder || 100000 : menuAnchorOrder(a.name)) -
      (isCustomMailFolder(b) ? b.sortOrder || 100000 : menuAnchorOrder(b.name)) ||
    a.name.localeCompare(b.name)
  )
}

function assignCustomFolderOrders(items: MailMenuItem[]) {
  const out: { id: string; sortOrder: number }[] = []
  for (let i = 0; i < items.length; i++) {
    const item = items[i]
    if (!isCustomMenuFolder(item)) continue
    let start = 0
    for (let j = i - 1; j >= 0; j--) {
      if (!isCustomMenuFolder(items[j])) {
        start = items[j].order
        break
      }
    }
    const groupStart = i
    let groupEnd = i
    while (groupEnd + 1 < items.length && isCustomMenuFolder(items[groupEnd + 1])) groupEnd++
    let end = start + (groupEnd - groupStart + 2) * 1000
    for (let j = groupEnd + 1; j < items.length; j++) {
      if (!isCustomMenuFolder(items[j])) {
        end = items[j].order
        break
      }
    }
    const step = Math.max(1, Math.floor((end - start) / (groupEnd - groupStart + 2)))
    for (let j = groupStart; j <= groupEnd; j++) {
      const folder = items[j] as Extract<MailMenuItem, { type: "folder" }>
      out.push({ id: folder.folderId, sortOrder: start + step * (j - groupStart + 1) })
    }
    i = groupEnd
  }
  return out
}

function isCustomMenuFolder(item: MailMenuItem): item is Extract<MailMenuItem, { type: "folder" }> {
  return item.type === "folder" && item.custom
}

function menuAnchorOrder(name: string) {
  switch (name) {
    case "Inbox":
      return 1000
    case "Drafts":
      return 3000
    case "Sent":
      return 4000
    case "Archive":
      return 5000
    case "Trash":
      return 7000
    case "Spam":
      return 8000
    default:
      return 100000
  }
}

function FolderSkeleton() {
  useUiLanguage()

  return (
    <div className="space-y-2 p-2">
      <Skeleton className="h-8 w-full" />
      <Skeleton className="h-8 w-4/5" />
      <Skeleton className="h-8 w-3/4" />
    </div>
  )
}
function MessageSkeleton() {
  useUiLanguage()

  return (
    <div className="space-y-0">
      {Array.from({ length: 6 }).map((_, i) => (
        <div className="space-y-2 border-b p-4" key={i}>
          <Skeleton className="h-4 w-1/2" />
          <Skeleton className="h-4 w-4/5" />
          <Skeleton className="h-3 w-full" />
        </div>
      ))}
    </div>
  )
}

function getEmptyMessage(mailView: MailView, folder: string, total: number) {
  if (mailView === "external")
    return total === 0 ? "远端文件夹没有邮件" : "当前筛选条件下没有远端邮件"
  if (mailView === "scheduled") return total === 0 ? "没有待发送邮件" : "当前搜索没有匹配的定时邮件"
  if (mailView === "sendQueue") return total === 0 ? "发送队列为空" : "当前搜索没有匹配的发送任务"
  if (total > 0) return "当前筛选条件下没有邮件"
  if (mailView === "starred") return "暂无星标邮件"
  if (mailView === "label") return "当前标签没有邮件"
  if (folder === "Inbox") return "收件箱暂时为空"
  if (folder === "Drafts") return "还没有草稿"
  if (folder === "Sent") return "还没有已发送邮件"
  if (folder === "Trash") return "回收站是空的"
  if (folder === "Spam") return "暂无垃圾邮件"
  return "当前文件夹没有邮件"
}

function externalAccountLabel(account: ExternalImapAccount) {
  return account.oauthEmail || account.username || account.name || uiText("外部邮箱")
}

function externalAccountSubtitle(account: ExternalImapAccount) {
  const label = externalAccountLabel(account)
  const mode = uiText(account.storageMode === "local" ? "同步" : "直连")
  const name = account.name && account.name !== label ? account.name : ""
  return [name, account.host, mode].filter(Boolean).join(" · ")
}

function NoMailboxState({ onOpenSettings }: { onOpenSettings: () => void }) {
  useUiLanguage()

  return (
    <div className="grid min-h-0 flex-1 place-items-center p-6">
      <div className="w-full max-w-md rounded-lg border border-dashed p-8 text-center">
        <div className="mx-auto mb-4 grid size-12 place-items-center rounded-full bg-muted">
          <Mail className="h-5 w-5 text-muted-foreground" />
        </div>
        <div className="text-lg font-semibold">{uiText("还没有可用邮箱")}</div>
        <div className="mt-2 text-sm text-muted-foreground">
          {uiText("请在个人中心申请邮箱，或联系管理员为当前账号分配邮箱。")}
        </div>
        <Button className="mt-5" onClick={onOpenSettings}>
          <Settings className="h-4 w-4" />
          {uiText("前往个人中心")}
        </Button>
      </div>
    </div>
  )
}

function PermissionEmptyState({
  title,
  description,
  onOpenSettings,
}: {
  title: string
  description: string
  onOpenSettings: () => void
}) {
  useUiLanguage()

  return (
    <div className="grid min-h-0 flex-1 place-items-center p-6">
      <div className="w-full max-w-md rounded-lg border border-dashed p-8 text-center">
        <div className="mx-auto mb-4 grid size-12 place-items-center rounded-full bg-muted">
          <ShieldCheck className="h-5 w-5 text-muted-foreground" />
        </div>
        <div className="text-lg font-semibold">{uiText(title)}</div>
        <div className="mt-2 text-sm text-muted-foreground">{uiText(description)}</div>
        <Button className="mt-5" onClick={onOpenSettings}>
          <Settings className="h-4 w-4" />
          {uiText("前往个人中心")}
        </Button>
      </div>
    </div>
  )
}

function ScheduledSendView({
  compact,
  items,
  total,
  loading,
  query,
  cancelingId,
  onCancel,
}: {
  compact: boolean
  items: ScheduledSend[]
  total: number
  loading: boolean
  query: string
  cancelingId: string
  onCancel: (item: ScheduledSend) => void
}) {
  useUiLanguage()

  const empty = query.trim() ? "当前搜索没有匹配的定时邮件" : "没有待发送邮件"
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div
        className={cn(
          "flex shrink-0 items-center justify-between gap-3 border-b",
          compact ? "h-12 px-4" : "h-14 px-5"
        )}
      >
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <Clock3 className="h-4 w-4" />
            {uiText("待发送")}
          </div>
          <div className="text-xs text-muted-foreground">
            {uiText("{0} / {1} 封定时邮件", [items.length, total])}
          </div>
        </div>
      </div>
      <ScrollArea className="min-h-0 flex-1">
        {loading && <ScheduledSendSkeleton />}
        {!loading && items.length === 0 && (
          <div className="p-8 text-center text-sm text-muted-foreground">{empty}</div>
        )}
        {!loading &&
          items.map((item) => (
            <ScheduledSendRow
              key={item.id}
              item={item}
              compact={compact}
              pending={cancelingId === item.id}
              onCancel={() => onCancel(item)}
            />
          ))}
      </ScrollArea>
    </div>
  )
}

function ScheduledSendSkeleton() {
  useUiLanguage()

  return (
    <div className="space-y-0">
      {Array.from({ length: 4 }).map((_, index) => (
        <div className="space-y-3 border-b p-4" key={index}>
          <Skeleton className="h-4 w-1/2" />
          <Skeleton className="h-3 w-3/4" />
          <Skeleton className="h-8 w-32" />
        </div>
      ))}
    </div>
  )
}

function ScheduledSendRow({
  item,
  compact,
  pending,
  onCancel,
}: {
  item: ScheduledSend
  compact: boolean
  pending: boolean
  onCancel: () => void
}) {
  useUiLanguage()

  const recipients = item.to?.length ? item.to.join(", ") : "未填写收件人"
  const failed = item.status === "failed"
  return (
    <div
      className={cn("border-b transition-colors hover:bg-accent/40", compact ? "p-4" : "px-5 py-4")}
    >
      <div
        className={cn(
          "gap-4",
          compact ? "space-y-3" : "grid grid-cols-[minmax(0,1fr)_180px_116px] items-center"
        )}
      >
        <div className="min-w-0">
          <div className="mb-1 flex min-w-0 items-center gap-2">
            <span className="truncate text-sm font-semibold">
              {item.subject || uiText("(无主题)")}
            </span>
            <ScheduledStatusBadge status={item.status} />
          </div>
          <div className="truncate text-xs text-muted-foreground">
            {uiText("发给 {0}", [recipients])}
          </div>
          {item.snippet && (
            <div className="mt-1 line-clamp-1 text-xs text-muted-foreground">{item.snippet}</div>
          )}
          {failed && item.error && (
            <div className="mt-2 text-xs text-destructive">{uiText(errorMessage(item.error))}</div>
          )}
        </div>
        <div className="text-sm">
          <div className="text-xs text-muted-foreground">{uiText("发送时间")}</div>
          <div className="mt-1 font-medium">{formatDateTime(item.sendAt)}</div>
        </div>
        <div className={cn("flex", compact ? "justify-start" : "justify-end")}>
          <Button
            type="button"
            variant={failed ? "outline" : "destructive"}
            size="sm"
            disabled={pending || item.status === "sending"}
            onClick={onCancel}
          >
            {pending ? uiText("处理中...") : failed ? uiText("移除记录") : uiText("取消发送")}
          </Button>
        </div>
      </div>
    </div>
  )
}

function ScheduledStatusBadge({ status }: { status: ScheduledSend["status"] }) {
  useUiLanguage()

  const label =
    status === "pending"
      ? "等待发送"
      : status === "sending"
        ? "发送中"
        : status === "failed"
          ? "发送失败"
          : status === "sent"
            ? "已发送"
            : "已取消"
  return (
    <Badge
      variant={status === "failed" ? "destructive" : status === "sending" ? "secondary" : "outline"}
      className="h-5 shrink-0 rounded-lg px-1.5 text-[11px] font-normal"
    >
      {uiText(label)}
    </Badge>
  )
}

const sendQueueStatusOptions: { value: SendQueueStatus | "all"; label: string }[] = [
  { value: "all", label: "全部状态" },
  { value: "queued", label: "排队中" },
  { value: "sending", label: "发送中" },
  { value: "failed", label: "发送失败" },
  { value: "delivered", label: "已投递" },
  { value: "canceled", label: "已取消" },
]

function SendQueueView({
  mobile,
  dense,
  items,
  total,
  loading,
  status,
  messageId,
  recipient,
  from,
  to,
  pendingId,
  onStatusChange,
  onMessageIdChange,
  onRecipientChange,
  onFromChange,
  onToChange,
  onClearFilters,
  onRetry,
  onCancel,
  onAudit,
  canMutate,
}: {
  mobile: boolean
  dense: boolean
  items: SendQueueItem[]
  total: number
  loading: boolean
  status: SendQueueStatus | "all"
  messageId: string
  recipient: string
  from: string
  to: string
  pendingId: string
  onStatusChange: (status: SendQueueStatus | "all") => void
  onMessageIdChange: (value: string) => void
  onRecipientChange: (value: string) => void
  onFromChange: (value: string) => void
  onToChange: (value: string) => void
  onClearFilters: () => void
  onRetry: (item: SendQueueItem) => void
  onCancel: (item: SendQueueItem) => void
  onAudit: (item: SendQueueItem) => void
  canMutate: boolean
}) {
  useUiLanguage()

  const [filtersOpen, setFiltersOpen] = React.useState(false)
  const hasFilters = status !== "all" || messageId.trim() || recipient.trim() || from || to
  const activeFilterCount = [
    status !== "all",
    !!messageId.trim(),
    !!recipient.trim(),
    !!from,
    !!to,
  ].filter(Boolean).length
  const empty = hasFilters ? "当前筛选没有匹配的发送任务" : "发送队列为空"
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div className={cn("shrink-0 border-b bg-muted/15", mobile ? "px-4 py-3" : "px-5 py-3")}>
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-base font-semibold">
              <History className="h-4 w-4" />
              {uiText("发送队列")}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {uiText("{0} / {1} 个发送任务", [items.length, total])}
            </div>
          </div>
          <div className="flex items-center gap-2">
            {mobile && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-9 gap-1.5"
                onClick={() => setFiltersOpen((open) => !open)}
                aria-expanded={filtersOpen}
              >
                <SlidersHorizontal className="h-4 w-4" />
                {uiText("筛选")}
                {activeFilterCount > 0 && (
                  <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-foreground px-1 text-[11px] text-background">
                    {uiText("{0}", [activeFilterCount])}
                  </span>
                )}
              </Button>
            )}
            <Select
              value={status}
              onValueChange={(value) => onStatusChange(value as SendQueueStatus | "all")}
            >
              <SelectTrigger className="h-9 w-[116px] bg-background sm:w-[132px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sendQueueStatusOptions.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {uiText(item.label)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        {(!mobile || filtersOpen) && (
          <div
            className={cn(
              "mt-3 grid gap-2",
              mobile
                ? "grid-cols-1"
                : "grid-cols-2 xl:grid-cols-[minmax(220px,1.15fr)_minmax(190px,1fr)_minmax(420px,1.5fr)_auto]"
            )}
          >
            <div className="relative min-w-0">
              <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input
                value={messageId}
                onChange={(event) => onMessageIdChange(event.target.value)}
                placeholder={uiText("搜索 Message-ID")}
                className="h-9 bg-background pl-9"
              />
            </div>
            <div className="relative min-w-0">
              <Mail className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input
                value={recipient}
                onChange={(event) => onRecipientChange(event.target.value)}
                placeholder={uiText("搜索收件人")}
                className="h-9 bg-background pl-9"
              />
            </div>
            <div
              className={cn(
                "flex min-w-0 items-center rounded-lg border bg-background px-2 shadow-sm focus-within:ring-1 focus-within:ring-ring",
                mobile ? "flex-col items-stretch gap-1 p-2" : "col-span-2 xl:col-span-1"
              )}
            >
              <div className="flex shrink-0 items-center gap-1.5 px-1 text-xs text-muted-foreground">
                <Clock3 className="h-4 w-4" />
                {uiText("时间")}
              </div>
              <Input
                aria-label={uiText("开始时间")}
                type="datetime-local"
                value={from}
                onChange={(event) => onFromChange(event.target.value)}
                className="h-8 min-w-0 border-0 px-2 shadow-none focus-visible:ring-0"
              />
              {!mobile && (
                <span className="shrink-0 text-xs text-muted-foreground">{uiText("至")}</span>
              )}
              <Input
                aria-label={uiText("结束时间")}
                type="datetime-local"
                value={to}
                onChange={(event) => onToChange(event.target.value)}
                className="h-8 min-w-0 border-0 px-2 shadow-none focus-visible:ring-0"
              />
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className={cn("h-9 w-9 text-muted-foreground", mobile && "justify-self-end")}
              disabled={!hasFilters}
              onClick={onClearFilters}
              title={uiText("重置筛选")}
              aria-label={uiText("重置筛选")}
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
        )}
      </div>
      <ScrollArea className="min-h-0 flex-1">
        {loading && <ScheduledSendSkeleton />}
        {!loading && items.length === 0 && (
          <div className="flex min-h-52 flex-col items-center justify-center px-6 py-10 text-center">
            <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <History className="h-5 w-5" />
            </div>
            <div className="text-sm font-medium">{empty}</div>
            {hasFilters && (
              <Button
                type="button"
                variant="link"
                size="sm"
                className="mt-1 text-muted-foreground"
                onClick={onClearFilters}
              >
                {uiText("重置筛选")}
              </Button>
            )}
          </div>
        )}
        {!loading && !mobile && items.length > 0 && (
          <div className="sticky top-0 z-10 hidden grid-cols-[120px_minmax(260px,1.5fr)_minmax(180px,0.75fr)_190px_104px] items-center gap-4 border-b bg-background/95 px-5 py-2 text-[11px] font-medium text-muted-foreground backdrop-blur xl:grid">
            <div>{uiText("状态")}</div>
            <div>{uiText("邮件")}</div>
            <div>{uiText("投递信息")}</div>
            <div>{uiText("更新时间")}</div>
            <div className="text-right">{uiText("操作")}</div>
          </div>
        )}
        {!loading &&
          items.map((item) => (
            <SendQueueRow
              key={item.id}
              item={item}
              mobile={mobile}
              dense={dense}
              pending={pendingId === item.id}
              onRetry={() => onRetry(item)}
              onCancel={() => onCancel(item)}
              onAudit={() => onAudit(item)}
              canMutate={canMutate}
            />
          ))}
      </ScrollArea>
    </div>
  )
}

function SendQueueRow({
  item,
  mobile,
  dense,
  pending,
  onRetry,
  onCancel,
  onAudit,
  canMutate,
}: {
  item: SendQueueItem
  mobile: boolean
  dense: boolean
  pending: boolean
  onRetry: () => void
  onCancel: () => void
  onAudit: () => void
  canMutate: boolean
}) {
  useUiLanguage()

  const recipients = item.recipients?.length ? item.recipients.join(", ") : "未记录收件人"
  const failure = item.lastError || item.error || item.failureReason || ""
  const canRetry = item.status === "failed"
  const canCancel = item.status === "queued" || item.status === "failed"
  return (
    <div
      className={cn(
        "border-b transition-colors hover:bg-accent/30",
        mobile ? "px-4 py-4" : dense ? "px-5 py-2.5" : "px-5 py-3.5"
      )}
    >
      <div
        className={cn(
          "gap-4 space-y-3 xl:grid xl:grid-cols-[120px_minmax(260px,1.5fr)_minmax(180px,0.75fr)_190px_104px] xl:items-center xl:space-y-0"
        )}
      >
        <div className="flex items-center justify-between gap-3">
          <SendQueueStatusBadge status={item.status} />
          <span className="text-xs text-muted-foreground xl:hidden">
            {formatDateTime(item.updatedAt || item.createdAt)}
          </span>
        </div>
        <div className="min-w-0">
          <div
            className="truncate text-sm font-semibold"
            title={item.subject || uiText("(无主题)")}
          >
            {item.subject || uiText("(无主题)")}
          </div>
          <div className="mt-1 truncate text-xs text-muted-foreground" title={recipients}>
            {uiText("发给 {0}", [recipients])}
          </div>
          {failure && (
            <div
              className="mt-2 line-clamp-2 rounded bg-destructive/[0.08] px-2 py-1 text-xs text-destructive"
              title={uiText(errorMessage(failure))}
            >
              {uiText(errorMessage(failure))}
            </div>
          )}
        </div>
        <div className="min-w-0 text-xs">
          <div className="truncate font-medium" title={uiText(sendQueueSourceLabel(item.source))}>
            {uiText(sendQueueSourceLabel(item.source))}
          </div>
          <div className="mt-1 text-muted-foreground">
            {uiText("尝试 {0}/{1}", [item.attemptCount, item.maxAttempts])}
          </div>
          {item.nextAttemptAt && (
            <div
              className="mt-1 truncate text-muted-foreground"
              title={formatDateTime(item.nextAttemptAt)}
            >
              {uiText("下次 {0}", [formatDateTime(item.nextAttemptAt)])}
            </div>
          )}
        </div>
        <div className="hidden min-w-0 text-xs xl:block">
          <div className="font-medium text-foreground">
            {formatDateTime(item.updatedAt || item.createdAt)}
          </div>
          {item.deliveredAt && (
            <div className="mt-1 truncate text-muted-foreground">
              {uiText("投递于 {0}", [formatDateTime(item.deliveredAt)])}
            </div>
          )}
        </div>
        <div className="flex items-center justify-end gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-muted-foreground"
            onClick={onAudit}
            title={uiText("查看时间线")}
            aria-label={uiText("查看时间线")}
          >
            <History className="h-4 w-4" />
          </Button>
          {canMutate && canRetry && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-8 w-8 text-muted-foreground hover:text-foreground"
              disabled={pending}
              onClick={onRetry}
              title={pending ? uiText("处理中...") : uiText("重试任务")}
              aria-label={pending ? uiText("处理中...") : uiText("重试任务")}
            >
              <RotateCcw className={cn("h-4 w-4", pending && "animate-spin")} />
            </Button>
          )}
          {canMutate && canCancel && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-muted-foreground"
                  disabled={pending}
                  title={uiText("更多操作")}
                  aria-label={uiText("更多操作")}
                >
                  <Ellipsis className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  onSelect={onCancel}
                >
                  <X className="h-4 w-4" />
                  {uiText("取消任务")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>
    </div>
  )
}

function SendQueueStatusBadge({ status }: { status: SendQueueStatus }) {
  useUiLanguage()

  const label =
    status === "queued"
      ? "排队中"
      : status === "sending"
        ? "发送中"
        : status === "delivered"
          ? "已投递"
          : status === "failed"
            ? "发送失败"
            : "已取消"
  const styles =
    status === "queued"
      ? "border-zinc-200 bg-zinc-50 text-zinc-700 dark:border-zinc-700 dark:bg-zinc-900/60 dark:text-zinc-300"
      : status === "sending"
        ? "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-300"
        : status === "delivered"
          ? "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/50 dark:text-emerald-300"
          : status === "failed"
            ? "border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/50 dark:text-red-300"
            : "border-zinc-200 bg-zinc-100 text-zinc-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-400"
  return (
    <Badge
      variant="outline"
      className={cn("h-6 shrink-0 gap-1.5 rounded-lg px-2 text-[11px] font-medium", styles)}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {uiText(label)}
    </Badge>
  )
}

function SendQueueAuditDialog({
  open,
  loading,
  events,
  onOpenChange,
}: {
  open: boolean
  loading: boolean
  events: SendQueueAuditEvent[]
  onOpenChange: (open: boolean) => void
}) {
  useUiLanguage()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(92vw,42rem)] max-w-none">
        <DialogHeader>
          <DialogTitle>{uiText("投递时间线")}</DialogTitle>
        </DialogHeader>
        <div className="max-h-[60vh] overflow-auto pr-1">
          {loading && (
            <div className="space-y-3">
              {Array.from({ length: 4 }).map((_, index) => (
                <Skeleton key={index} className="h-14 w-full" />
              ))}
            </div>
          )}
          {!loading && events.length === 0 && (
            <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
              {uiText("暂无投递事件")}
            </div>
          )}
          {!loading && events.length > 0 && (
            <div className="ml-2 border-l">
              {events.map((event) => (
                <div key={event.id} className="relative pb-5 pl-5 last:pb-0">
                  <span className="absolute -left-[5px] top-1.5 h-2.5 w-2.5 rounded-full border-2 border-background bg-foreground" />
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2 text-sm font-medium">
                      {event.status && <SendQueueStatusBadge status={event.status} />}
                      <span>
                        {event.message || event.event || event.eventType || uiText("队列事件")}
                      </span>
                    </div>
                    <span className="text-xs text-muted-foreground">
                      {formatDateTime(event.createdAt)}
                    </span>
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    {typeof event.attemptCount === "number" && (
                      <span>{uiText("尝试次数：{0}", [event.attemptCount])}</span>
                    )}
                    {event.error && (
                      <span className="w-full rounded bg-destructive/[0.08] px-2 py-1.5 text-destructive">
                        {uiText(errorMessage(event.error))}
                      </span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {uiText("关闭")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function sendQueueSourceLabel(source: string) {
  const normalized = source.toLowerCase()
  if (normalized === "submission") return "SMTP Submission"
  if (normalized === "webmail") return "Webmail"
  if (normalized === "open_api") return "Open API"
  if (normalized === "scheduled") return "定时发送"
  if (normalized === "forward_verification") return "转发邮箱验证"
  if (normalized.startsWith("rule_forward:")) return "收件规则自动转发"
  return source || "未知"
}

function datetimeLocalToISO(value: string) {
  if (!value) return ""
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? "" : date.toISOString()
}

type BulkAction = "read" | "unread" | "star" | "unstar" | "archive" | "trash" | "spam" | "delete"

function BulkActionMenu({
  pending,
  onAction,
}: {
  pending: boolean
  onAction: (action: BulkAction) => void
}) {
  useUiLanguage()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" disabled={pending}>
          {uiText("批量操作")}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={() => onAction("read")}>{uiText("标为已读")}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("unread")}>
          {uiText("标为未读")}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("star")}>{uiText("添加星标")}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("unstar")}>
          {uiText("取消星标")}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("archive")}>{uiText("归档")}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("trash")}>
          {uiText("移入回收站")}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("spam")}>
          {uiText("移入垃圾邮件")}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onAction("delete")} className="text-destructive">
          {uiText("删除")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function SidebarContextMenu({
  state,
  canOrganize,
  pending,
  onClose,
  onOpen,
  onRefresh,
  onCreateFolder,
  onMove,
  onDelete,
}: {
  state: SidebarContextMenuState | null
  canOrganize: boolean
  pending: boolean
  onClose: () => void
  onOpen: (item: MailMenuItem) => void
  onRefresh: () => void
  onCreateFolder: () => void
  onMove: (item: MailMenuItem, action: "top" | "up" | "down" | "bottom") => void
  onDelete: (item: MailMenuItem) => void
}) {
  useUiLanguage()

  React.useEffect(() => {
    if (!state) return
    const close = () => onClose()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose()
    }
    window.addEventListener("pointerdown", close)
    window.addEventListener("resize", close)
    window.addEventListener("scroll", close, true)
    window.addEventListener("keydown", onKeyDown)
    return () => {
      window.removeEventListener("pointerdown", close)
      window.removeEventListener("resize", close)
      window.removeEventListener("scroll", close, true)
      window.removeEventListener("keydown", onKeyDown)
    }
  }, [state, onClose])

  if (!state) return null
  const item = state.item
  const customFolder = item.type === "folder" && item.custom
  const position = contextMenuPosition(state.x, state.y)
  const itemClass = "h-auto w-full justify-start rounded px-2 py-1.5 text-left font-normal"
  return (
    <div
      className="fixed z-[110] w-48 rounded-xl border bg-popover p-1 text-sm text-popover-foreground shadow-md"
      style={{ left: position.x, top: position.y }}
      onPointerDown={(event) => event.stopPropagation()}
      onContextMenu={(event) => event.preventDefault()}
      role="menu"
    >
      <Button type="button" variant="ghost" className={itemClass} onClick={() => onOpen(item)}>
        <Inbox className="h-4 w-4" />
        {uiText("打开")}
      </Button>
      <Button type="button" variant="ghost" className={itemClass} onClick={onRefresh}>
        <RefreshCcw className="h-4 w-4" />
        {uiText("刷新")}
      </Button>
      {canOrganize && (
        <Button type="button" variant="ghost" className={itemClass} onClick={onCreateFolder}>
          <Plus className="h-4 w-4" />
          {uiText("新建文件夹")}
        </Button>
      )}
      {canOrganize && customFolder && (
        <>
          <div className="my-1 h-px bg-border" />
          <Button
            type="button"
            variant="ghost"
            className={itemClass}
            disabled={pending}
            onClick={() => onMove(item, "top")}
          >
            <ArrowLeft className="h-4 w-4 rotate-90" />
            {uiText("移到最上")}
          </Button>
          <Button
            type="button"
            variant="ghost"
            className={itemClass}
            disabled={pending}
            onClick={() => onMove(item, "up")}
          >
            <ChevronDown className="h-4 w-4 rotate-180" />
            {uiText("上移一位")}
          </Button>
          <Button
            type="button"
            variant="ghost"
            className={itemClass}
            disabled={pending}
            onClick={() => onMove(item, "down")}
          >
            <ChevronDown className="h-4 w-4" />
            {uiText("下移一位")}
          </Button>
          <Button
            type="button"
            variant="ghost"
            className={itemClass}
            disabled={pending}
            onClick={() => onMove(item, "bottom")}
          >
            <ArrowLeft className="h-4 w-4 -rotate-90" />
            {uiText("移到最下")}
          </Button>
          <div className="my-1 h-px bg-border" />
          <Button
            type="button"
            variant="ghost"
            className={cn(
              itemClass,
              "text-destructive hover:bg-destructive/10 hover:text-destructive"
            )}
            onClick={() => onDelete(item)}
          >
            <Trash2 className="h-4 w-4" />
            {uiText("删除文件夹")}
          </Button>
        </>
      )}
    </div>
  )
}

function MessageContextMenu({
  state,
  labels,
  folders,
  canSend,
  canOrganize,
  canManageBlocked,
  canManageLabels,
  labelPending,
  onClose,
  onAction,
  onMoveToFolder,
  onToggleLabel,
  onAllowSender,
}: {
  state: MessageContextMenuState | null
  labels: MailLabel[]
  folders: MailFolder[]
  canSend: boolean
  canOrganize: boolean
  canManageBlocked: boolean
  canManageLabels: boolean
  labelPending: boolean
  onClose: () => void
  onAction: (
    action:
      "open" | "reply" | "forward" | "read" | "star" | "archive" | "trash" | "spam" | "delete",
    message: MailMessage
  ) => void
  onMoveToFolder: (message: MailMessage, folderName: string) => void
  onToggleLabel: (message: MailMessage, label: MailLabel) => void
  onAllowSender: (message: MailMessage) => void
}) {
  useUiLanguage()

  React.useEffect(() => {
    if (!state) return
    const close = () => onClose()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose()
    }
    window.addEventListener("pointerdown", close)
    window.addEventListener("resize", close)
    window.addEventListener("scroll", close, true)
    window.addEventListener("keydown", onKeyDown)
    return () => {
      window.removeEventListener("pointerdown", close)
      window.removeEventListener("resize", close)
      window.removeEventListener("scroll", close, true)
      window.removeEventListener("keydown", onKeyDown)
    }
  }, [state, onClose])

  if (!state) return null
  const { message } = state
  const position = contextMenuPosition(state.x, state.y)
  const draft = message.folder === "Drafts"
  const itemClass = "h-auto w-full justify-start rounded-lg px-3 py-2 text-left font-normal"

  function item(
    label: string,
    icon: React.ReactNode,
    action: Parameters<typeof onAction>[0],
    destructive = false
  ) {
    return (
      <Button
        type="button"
        variant="ghost"
        className={cn(itemClass, destructive && "text-destructive hover:text-destructive")}
        onClick={() => onAction(action, message)}
      >
        {icon}
        <span>{uiText(label)}</span>
      </Button>
    )
  }
  function toggleLabel(label: MailLabel) {
    onToggleLabel(message, label)
    onClose()
  }
  function moveToFolder(folderName: string) {
    onMoveToFolder(message, folderName)
    onClose()
  }
  const movableFolders = folders.filter(
    (folder) => folder.name !== message.folder && folder.name !== "Drafts"
  )

  return (
    <div
      className="fixed z-[110] w-52 rounded-xl border bg-popover p-1 text-popover-foreground shadow-md"
      style={{ left: position.x, top: position.y }}
      onClick={(event) => event.stopPropagation()}
      onContextMenu={(event) => event.preventDefault()}
      onPointerDown={(event) => event.stopPropagation()}
      role="menu"
    >
      {item(
        draft ? uiText("编辑草稿") : uiText("打开邮件"),
        draft ? <PencilLine className="h-4 w-4" /> : <Mail className="h-4 w-4" />,
        "open"
      )}
      {!draft && canSend && (
        <>
          {item(uiText("回复"), <Reply className="h-4 w-4" />, "reply")}
          {item(uiText("转发"), <Forward className="h-4 w-4" />, "forward")}
        </>
      )}
      {!draft && canOrganize && (
        <>
          <div className="-mx-1 my-1 h-px bg-border" />
          {item(
            message.isRead ? uiText("标为未读") : uiText("标为已读"),
            <MailCheck className="h-4 w-4" />,
            "read"
          )}
          {item(
            message.isStarred ? uiText("取消星标") : uiText("添加星标"),
            <Star
              className={cn("h-4 w-4", message.isStarred && "fill-yellow-400 text-yellow-500")}
            />,
            "star"
          )}
          {item(
            message.folder === "Archive" ? uiText("取消归档") : uiText("归档"),
            <Archive className="h-4 w-4" />,
            "archive"
          )}
          {message.folder !== "Trash" &&
            item(uiText("移入回收站"), <Trash2 className="h-4 w-4" />, "trash")}
          {message.folder !== "Spam" &&
            item(uiText("移入垃圾邮件"), <Trash2 className="h-4 w-4" />, "spam")}
          {canManageBlocked && (
            <Button
              type="button"
              variant="ghost"
              className={itemClass}
              onClick={() => {
                onAllowSender(message)
                onClose()
              }}
            >
              <ShieldCheck className="h-4 w-4" />
              <span>{uiText("加入白名单")}</span>
            </Button>
          )}
        </>
      )}
      {!draft && canOrganize && movableFolders.length > 0 && (
        <>
          <div className="-mx-1 my-1 h-px bg-border" />
          <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground">
            {uiText("移动到")}
          </div>
          <div className="max-h-44 overflow-y-auto">
            {movableFolders.map((folder) => (
              <Button
                key={folder.id}
                type="button"
                variant="ghost"
                className={itemClass}
                onClick={() => moveToFolder(folder.name)}
              >
                {folderIcons[folder.role] || <Inbox className="h-4 w-4" />}
                <span className="min-w-0 flex-1 truncate text-left">
                  {folderLabels[folder.name] ? uiText(folderLabels[folder.name]) : folder.name}
                </span>
                {folder.totalCount > 0 && (
                  <span className="text-xs text-muted-foreground">
                    {uiText("{0}", [folder.totalCount])}
                  </span>
                )}
              </Button>
            ))}
          </div>
        </>
      )}
      {canManageLabels && labels.length > 0 && (
        <>
          <div className="-mx-1 my-1 h-px bg-border" />
          <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground">
            {uiText("标签")}
          </div>
          <div className="max-h-44 overflow-y-auto">
            {labels.map((label) => {
              const active = (message.labels || []).some((item) => item.id === label.id)
              const colors = { backgroundColor: labelDotColor(label) }
              return (
                <Button
                  key={label.id}
                  type="button"
                  variant="ghost"
                  className={itemClass}
                  disabled={labelPending}
                  onClick={() => toggleLabel(label)}
                >
                  <Check className={cn("h-4 w-4", active ? "opacity-100" : "opacity-0")} />
                  <span
                    className="h-2 w-2 shrink-0 rounded-full"
                    style={{ backgroundColor: colors.backgroundColor }}
                  />
                  <span className="min-w-0 flex-1 truncate text-left">
                    {active ? uiText("移除 {0}", [label.name]) : label.name}
                  </span>
                </Button>
              )
            })}
          </div>
        </>
      )}
      {canOrganize && (
        <>
          <div className="-mx-1 my-1 h-px bg-border" />
          {item(uiText("删除"), <Trash2 className="h-4 w-4" />, "delete", true)}
        </>
      )}
    </div>
  )
}

function contextMenuPosition(x: number, y: number) {
  const width = 208
  const height = 312
  const padding = 8
  const maxX = Math.max(padding, window.innerWidth - width - padding)
  const maxY = Math.max(padding, window.innerHeight - height - padding)
  return { x: Math.min(Math.max(x, padding), maxX), y: Math.min(Math.max(y, padding), maxY) }
}

function CreateFolderDialog({
  open,
  pending,
  onOpenChange,
  onCreate,
}: {
  open: boolean
  pending: boolean
  onOpenChange: (open: boolean) => void
  onCreate: (name: string) => void
}) {
  useUiLanguage()

  const [name, setName] = React.useState("")
  React.useEffect(() => {
    if (open) setName("")
  }, [open])
  const trimmed = name.trim()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(92vw,28rem)] max-w-none">
        <DialogHeader>
          <DialogTitle>{uiText("新建文件夹")}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (trimmed) onCreate(trimmed)
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="new-folder-name">{uiText("文件夹名称")}</Label>
            <Input
              id="new-folder-name"
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={uiText("例如：客户、账单、项目归档")}
            />
          </div>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {uiText("取消")}
            </Button>
            <Button disabled={!trimmed || pending}>
              {pending ? uiText("创建中...") : uiText("创建")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function MailPagination({
  pageIndex,
  pageSize,
  itemCount,
  totalCount,
  hasPreviousPage,
  hasNextPage,
  loading,
  onPageSizeChange,
  onPreviousPage,
  onNextPage,
}: {
  pageIndex: number
  pageSize: MailPageSize
  itemCount: number
  totalCount?: number
  hasPreviousPage: boolean
  hasNextPage: boolean
  loading: boolean
  onPageSizeChange: (size: MailPageSize) => void
  onPreviousPage: () => void
  onNextPage: () => void
}) {
  useUiLanguage()

  const pageNumber = pageIndex + 1
  const totalPages =
    typeof totalCount === "number" ? Math.max(1, Math.ceil(totalCount / pageSize)) : undefined
  const rangeStart = itemCount > 0 ? pageIndex * pageSize + 1 : 0
  const rangeEnd =
    itemCount > 0 ? Math.min(totalCount ?? Number.MAX_SAFE_INTEGER, rangeStart + itemCount - 1) : 0
  return (
    <div className="flex min-h-12 shrink-0 items-center justify-between gap-2 border-t bg-background px-3 py-2 text-xs text-muted-foreground">
      <div className="min-w-0 truncate whitespace-nowrap">
        {typeof totalCount === "number"
          ? `${rangeStart}–${rangeEnd} / ${totalCount}`
          : uiText("第 {0} 页", [pageNumber])}
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <span className="hidden whitespace-nowrap min-[420px]:inline">{uiText("每页")}</span>
        <Select
          value={String(pageSize)}
          onValueChange={(value) => onPageSizeChange(Number(value) as MailPageSize)}
          disabled={loading}
        >
          <SelectTrigger
            className="h-8 w-[68px] bg-background px-2"
            aria-label={uiText("每页邮件数")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent align="end">
            <SelectItem value="20">20</SelectItem>
            <SelectItem value="30">30</SelectItem>
            <SelectItem value="50">50</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          disabled={!hasPreviousPage || loading}
          onClick={onPreviousPage}
          title={uiText("上一页")}
          aria-label={uiText("上一页")}
        >
          <ChevronLeft className="h-4 w-4" />
        </Button>
        <span className="min-w-10 whitespace-nowrap text-center text-foreground">
          {uiText("{0}", [pageNumber])}
          {totalPages ? ` / ${totalPages}` : ""}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          disabled={!hasNextPage || loading}
          onClick={onNextPage}
          title={uiText("下一页")}
          aria-label={uiText("下一页")}
        >
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

function CompactMailView({
  title,
  icon,
  messages,
  pageItemCount,
  total,
  selectedIds,
  allSelected,
  someSelected,
  loading,
  pageIndex,
  pageSize,
  hasPreviousPage,
  hasNextPage,
  pageLoading,
  emptyMessage,
  selectedId,
  selected,
  threadMessages,
  detailLoading,
  labels,
  labelPending,
  onSelect,
  onSelectAll,
  onToggleSelected,
  scheduledDraftIds,
  onPageSizeChange,
  onPreviousPage,
  onNextPage,
  onCloseReader,
  onStar,
  onReply,
  onForward,
  onSendTimeline,
  onArchive,
  onDelete,
  onToggleRead,
  onAddLabel,
  onRemoveLabel,
  bulkPending,
  onBulkAction,
  onContextMenu,
  canSend,
  canOrganize,
  canManageLabels,
  canDownloadAttachments,
  language,
}: {
  title: string
  icon?: React.ReactNode
  messages: MailMessage[]
  pageItemCount: number
  total?: number
  selectedIds: string[]
  allSelected: boolean
  someSelected: boolean
  loading: boolean
  pageIndex: number
  pageSize: MailPageSize
  hasPreviousPage: boolean
  hasNextPage: boolean
  pageLoading: boolean
  emptyMessage: string
  selectedId: string | null
  selected?: MailMessage
  threadMessages: MailMessage[]
  detailLoading: boolean
  labels: MailLabel[]
  labelPending: boolean
  onSelect: (id: string | null) => void
  onSelectAll: (checked: boolean) => void
  onToggleSelected: (id: string, checked: boolean) => void
  scheduledDraftIds: Set<string>
  onPageSizeChange: (size: MailPageSize) => void
  onPreviousPage: () => void
  onNextPage: () => void
  onCloseReader: () => void
  onStar: (message: MailMessage) => void
  onReply: (message: MailMessage) => void
  onForward: (message: MailMessage) => void
  onSendTimeline: (message: MailMessage) => void
  onArchive: (message: MailMessage) => void
  onDelete: (message: MailMessage) => void
  onToggleRead: (message: MailMessage) => void
  onAddLabel: (message: MailMessage, label: MailLabel) => void
  onRemoveLabel: (message: MailMessage, labelId: string) => void
  bulkPending: boolean
  onBulkAction: (action: BulkAction) => void
  onContextMenu: (event: React.MouseEvent, message: MailMessage) => void
  canSend: boolean
  canOrganize: boolean
  canManageLabels: boolean
  canDownloadAttachments: boolean
  language: Language
}) {
  useUiLanguage()

  const selectedIndex = selectedId ? messages.findIndex((message) => message.id === selectedId) : -1
  const previousMessage = selectedIndex > 0 ? messages[selectedIndex - 1] : undefined
  const nextMessage =
    selectedIndex >= 0 && selectedIndex < messages.length - 1
      ? messages[selectedIndex + 1]
      : undefined

  if (selectedId) {
    return (
      <CompactMessageDetail
        selected={selected}
        threadMessages={threadMessages}
        loading={detailLoading}
        labels={labels}
        labelPending={labelPending}
        previousMessage={previousMessage}
        nextMessage={nextMessage}
        onBack={onCloseReader}
        onSelect={onSelect}
        onStar={onStar}
        onReply={onReply}
        onForward={onForward}
        onSendTimeline={onSendTimeline}
        onArchive={onArchive}
        onDelete={onDelete}
        onToggleRead={onToggleRead}
        onAddLabel={onAddLabel}
        onRemoveLabel={onRemoveLabel}
        canSend={canSend}
        canOrganize={canOrganize}
        canManageLabels={canManageLabels}
        canDownloadAttachments={canDownloadAttachments}
        language={language}
      />
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div className="flex min-h-12 shrink-0 items-center justify-between gap-2 border-b px-3 sm:px-4">
        <div className="flex min-w-0 flex-1 items-center gap-3">
          {canOrganize && (
            <Checkbox
              aria-label={uiText("选择当前页邮件")}
              checked={allSelected ? true : someSelected ? "indeterminate" : false}
              onCheckedChange={(value) => onSelectAll(value === true)}
            />
          )}
          <div className="flex min-w-0 items-center gap-2 text-base font-semibold">
            {icon}
            <span className="truncate">{title}</span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {selectedIds.length > 0 ? (
            <>
              <span className="hidden text-sm text-muted-foreground min-[380px]:inline">
                {uiText("已选 {0} 封", [selectedIds.length])}
              </span>
              {canOrganize && <BulkActionMenu pending={bulkPending} onAction={onBulkAction} />}
            </>
          ) : (
            <div className="text-sm text-muted-foreground">
              {typeof total === "number"
                ? uiText("{0} / {1} 封", [messages.length, total])
                : uiText("{0} 封", [messages.length])}
            </div>
          )}
        </div>
      </div>
      <ScrollArea key={`compact-mail-page-${pageIndex}`} className="min-h-0 flex-1">
        {loading && <MessageSkeleton />}
        {messages.map((message) => (
          <CompactMessageRow
            key={message.id}
            message={message}
            active={selectedId === message.id}
            checked={selectedIds.includes(message.id)}
            scheduled={scheduledDraftIds.has(message.id)}
            onCheckedChange={(checked) => onToggleSelected(message.id, checked)}
            onClick={() => onSelect(message.id)}
            onContextMenu={(event) => onContextMenu(event, message)}
            onStar={() => onStar(message)}
            canOrganize={canOrganize}
          />
        ))}
        {!loading && messages.length === 0 && (
          <div className="p-8 text-center text-sm text-muted-foreground">
            {uiText(emptyMessage)}
          </div>
        )}
      </ScrollArea>
      <MailPagination
        pageIndex={pageIndex}
        pageSize={pageSize}
        itemCount={pageItemCount}
        totalCount={total}
        hasPreviousPage={hasPreviousPage}
        hasNextPage={hasNextPage}
        loading={pageLoading}
        onPageSizeChange={onPageSizeChange}
        onPreviousPage={onPreviousPage}
        onNextPage={onNextPage}
      />
    </div>
  )
}

function AccountHeader({
  collapsed,
  name,
  email,
  darkMode,
  onToggleTheme,
  onSettings,
}: {
  collapsed: boolean
  name: string
  email?: string
  darkMode: boolean
  onToggleTheme: () => void
  onSettings: () => void
}) {
  useUiLanguage()

  const displayName = cleanAccountName(name, email)
  if (collapsed) {
    return (
      <div className="flex justify-center">
        <Avatar className="size-8 rounded-full">
          <AvatarFallback className="bg-primary text-xs font-semibold text-primary-foreground">
            {accountInitial(displayName, email)}
          </AvatarFallback>
        </Avatar>
      </div>
    )
  }
  return (
    <div className="flex items-center justify-between gap-2">
      <div className="flex min-w-0 items-center gap-2">
        <Avatar className="size-8 rounded-full">
          <AvatarFallback className="bg-primary text-xs font-semibold text-primary-foreground">
            {accountInitial(displayName, email)}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0 text-sm">
          <div className="truncate text-sm font-semibold leading-5">{displayName}</div>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8 rounded-lg text-muted-foreground"
          aria-label={uiText(darkMode ? "切换到浅色" : "切换到深色")}
          onClick={onToggleTheme}
        >
          {darkMode ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8 rounded-lg text-muted-foreground"
          aria-label={uiText("设置")}
          onClick={onSettings}
        >
          <Settings className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

function MailboxSwitcher({
  collapsed,
  mailboxes,
  selectedMailbox,
  onSelect,
}: {
  collapsed: boolean
  mailboxes: Mailbox[]
  selectedMailbox?: Mailbox
  onSelect: (mailboxId: string) => void
}) {
  useUiLanguage()

  const owned = mailboxes.filter((mailbox) => mailbox.access !== "read")
  const shared = mailboxes.filter((mailbox) => mailbox.access === "read")
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          className={cn(
            "h-9 min-w-0 flex-1 justify-start gap-2 rounded-lg bg-background px-2 text-left font-normal",
            collapsed && "w-8 flex-none justify-center px-0"
          )}
        >
          <Mail className="h-4 w-4 shrink-0 text-muted-foreground" />
          {!collapsed && (
            <>
              <span className="min-w-0 flex-1 truncate text-sm font-medium">
                {selectedMailbox?.address || uiText("当前邮箱")}
              </span>
              {selectedMailbox?.access === "read" && (
                <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
                  {uiText("共享")}
                </Badge>
              )}
              <ChevronsUpDown className="h-4 w-4 shrink-0 text-muted-foreground" />
            </>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        {mailboxes.length === 0 && (
          <DropdownMenuItem disabled>{uiText("没有可用邮箱")}</DropdownMenuItem>
        )}
        {owned.length > 0 && (
          <DropdownMenuLabel className="text-xs text-muted-foreground">
            {uiText("我的邮箱")}
          </DropdownMenuLabel>
        )}
        {owned.map((mailbox) => (
          <DropdownMenuItem
            key={mailbox.id}
            onSelect={() => onSelect(mailbox.id)}
            className="gap-2"
          >
            <Check
              className={cn(
                "h-4 w-4",
                selectedMailbox?.id === mailbox.id ? "opacity-100" : "opacity-0"
              )}
            />
            <span className="min-w-0 flex-1 truncate font-medium">{mailbox.address}</span>
          </DropdownMenuItem>
        ))}
        {owned.length > 0 && shared.length > 0 && <DropdownMenuSeparator />}
        {shared.length > 0 && (
          <DropdownMenuLabel className="text-xs text-muted-foreground">
            {uiText("共享给我")}
          </DropdownMenuLabel>
        )}
        {shared.map((mailbox) => (
          <DropdownMenuItem
            key={mailbox.id}
            onSelect={() => onSelect(mailbox.id)}
            className="gap-2 py-2"
          >
            <Check
              className={cn(
                "h-4 w-4",
                selectedMailbox?.id === mailbox.id ? "opacity-100" : "opacity-0"
              )}
            />
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium">{mailbox.address}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {uiText("来自 {0}", [mailbox.sharedBy])}
              </span>
            </span>
            <Badge variant="outline" className="shrink-0 text-[10px]">
              {uiText("只读")}
            </Badge>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// 复制到剪贴板。navigator.clipboard 只在 HTTPS / localhost 可用，
// 局域网 http 部署（以及部分老浏览器）会拿到 undefined，这里回退到 execCommand。

// 只返回真正能解析出的地址；显示用的兜底文案交给 senderDisplayName，
// 否则调用方永远判断不出「拿不到地址」，会把「未知发件人地址」当成邮箱发出去。

function playIncomingMailSound(ref: React.MutableRefObject<AudioContext | null>) {
  const AudioContextCtor =
    window.AudioContext ||
    (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
  if (!AudioContextCtor) return
  const ctx = ref.current || new AudioContextCtor()
  ref.current = ctx
  if (ctx.state === "suspended") void ctx.resume()
  const now = ctx.currentTime
  const gain = ctx.createGain()
  gain.gain.setValueAtTime(0.0001, now)
  gain.gain.exponentialRampToValueAtTime(0.12, now + 0.02)
  gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.45)
  gain.connect(ctx.destination)
  for (const [index, frequency] of [880, 1175].entries()) {
    const osc = ctx.createOscillator()
    const start = now + index * 0.14
    osc.type = "sine"
    osc.frequency.setValueAtTime(frequency, start)
    osc.connect(gain)
    osc.start(start)
    osc.stop(start + 0.18)
  }
}
function quoteMessage(message: MailMessage, mode: "reply" | "forward" = "reply") {
  // 转发和回复用不同的分隔行：收件端只折叠「原始邮件」（回复），
  // 转发是整封内容的主体，折叠了就什么都看不见了。
  const marker = mode === "forward" ? "转发邮件" : "原始邮件"
  const headers = `----- ${marker} -----\nFrom: ${senderTitle(message)}\nTo: ${(message.to || []).join(", ")}\nDate: ${formatDateTime(message.receivedAt, "zh-CN")}\nSubject: ${message.subject}`
  // 有 HTML 就引用 HTML（图文、链接、排版都在），只有纯文本的邮件才退回文本引用
  if (message.bodyHtml && message.bodyHtml.trim()) {
    return quotedHtmlComposerValue(headers, message.bodyHtml, message.bodyText || "")
  }
  return quotedComposerValue(headers, message.bodyText || message.snippet || "")
}
async function attachmentFilesFromMessage(message: MailMessage) {
  if (!message.attachments?.length) return []
  return Promise.all(
    message.attachments.map(async (attachment) => {
      const response = await fetch(`/api/mail/attachments/${attachment.id}`, {
        credentials: "include",
      })
      // 403/404 的响应体是 JSON 错误文本，直接包成 File 会把错误页当附件发出去。
      if (!response.ok) {
        throw new Error(`attachment ${attachment.filename} download failed: ${response.status}`)
      }
      const blob = await response.blob()
      return new File([blob], attachment.filename, {
        type: attachment.contentType || blob.type || "application/octet-stream",
      })
    })
  )
}
