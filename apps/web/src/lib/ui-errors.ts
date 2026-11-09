const knownErrors: Record<string, string> = {
  "resource is a unified collection target; change or disable collection first":
    "请先更换或关闭引用此目标的统一收件设置",
  "collection target must be an active local mailbox without a forwarding alias":
    "请选择启用的站内邮箱，目标不能配置别名转发",
  "collection target cannot be a forwarding alias": "统一收件目标不能设置别名转发",
  unauthorized: "登录会话已失效，请重新登录",
  forbidden: "无权执行此操作",
  "permission required": "无权执行此操作",
  "external imap is disabled": "外部 IMAP 未启用",
  "invalid oauth state": "授权状态无效或已过期，请重试",
  "missing oauth code": "授权失败",
  "oauth exchange failed": "授权失败",
  "oauth account email does not match requested external email": "授权邮箱与填写的邮箱不一致",
  "delivery queue item is no longer eligible": "队列任务状态已变化，请刷新后重试",
  "failed to list folders": "无法读取文件夹，请稍后重试",
  "failed to load remote messages": "无法读取远端邮件，请稍后重试",
  "failed to load remote message": "无法读取远端邮件，请稍后重试",
  "failed to update remote message": "无法更新远端邮件，请稍后重试",
}

/** Keep a safe source message in query state; translate only when rendering it. */
export function errorMessage(error: unknown, status?: number): string {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : ""
  if (Object.prototype.hasOwnProperty.call(knownErrors, message)) return knownErrors[message]
  // 已经是中文的界面文案（例如服务端直接回的中文字符串）原样展示，
  // 不要再盖成「操作未完成」。以前靠词条表判断，词条表随语言锁定一起删了。
  if (containsCJK(message)) return message
  if (status === 401) return "登录会话已失效，请重新登录"
  if (status === 403) return "无权执行此操作"
  if (status === 404) return "请求的内容不存在或无权访问"
  if (status === 409) return "数据已变化，请刷新后重试"
  if (status === 429) return "请求过于频繁，请稍后重试"
  if (status === 400 || status === 422) return "请检查输入内容后重试"
  return "操作未完成，请稍后重试"
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: unknown
  ) {
    super(errorMessage(message, status))
    this.name = "ApiError"
  }
}

function containsCJK(value: string): boolean {
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0
    if (code >= 0x4e00 && code <= 0x9fff) return true
    if (code >= 0x3400 && code <= 0x4dbf) return true
  }
  return false
}
