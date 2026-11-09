import type { Editor } from "@tiptap/core"

import {
  htmlContainsMeaningfulContent,
  plainTextToHtml,
  sanitizeComposerHtml,
  type ComposerValue,
} from "@/components/mail-content"
import type { InsertDialogState } from "@/components/mail-composer-parts"

export const composerFontOptions = [
  "Arial",
  "Georgia",
  "Times New Roman",
  "Courier New",
  "Microsoft YaHei",
]
export const composerFontSizeOptions = [
  ["2", "小号"],
  ["3", "正文"],
  ["4", "中号"],
  ["5", "大号"],
] as const
export const composerFontSizeValueByKey: Record<string, string> = {
  "2": "13px",
  "3": "16px",
  "4": "20px",
  "5": "24px",
}
export const composerTextColors = [
  ["#111827", "默认"],
  ["#dc2626", "红色"],
  ["#2563eb", "蓝色"],
  ["#16a34a", "绿色"],
  ["#9333ea", "紫色"],
] as const
export const composerHighlightColors = [
  ["transparent", "无高亮"],
  ["#fef3c7", "黄色"],
  ["#dcfce7", "绿色"],
  ["#dbeafe", "蓝色"],
  ["#fce7f3", "粉色"],
] as const
export const composerEmojiOptions = [
  "😀",
  "😄",
  "😊",
  "🙂",
  "😉",
  "😍",
  "😘",
  "😎",
  "🤔",
  "👍",
  "👏",
  "🙏",
  "💪",
  "🎉",
  "🔥",
  "✨",
  "❤️",
  "✅",
  "📌",
  "📅",
  "☕",
  "💡",
  "🚀",
  "⭐",
]
export const composerMenuItemClass =
  "min-h-9 rounded-lg px-3 text-sm transition-colors data-[highlighted]:bg-primary/10 data-[highlighted]:font-semibold data-[highlighted]:text-foreground hover:bg-primary/10 hover:font-semibold hover:text-foreground"

export function normalizeFontName(value: string) {
  const cleaned = value.replace(/["']/g, "").split(",")[0]?.trim() || ""
  if (!cleaned || cleaned === "默认字体") return ""
  if (/microsoft yahei/i.test(cleaned) || cleaned.includes("微软雅黑")) return "Microsoft YaHei"
  const lower = cleaned.toLowerCase()
  return (
    composerFontOptions.find((font) => {
      const option = font.toLowerCase()
      return lower === option || lower.includes(option) || option.includes(lower)
    }) || ""
  )
}

export function normalizeFontSize(value: string) {
  const cleaned = value.trim().toLowerCase()
  if (!cleaned) return ""
  if (composerFontSizeOptions.some(([size]) => size === cleaned)) return cleaned
  const px = Number(cleaned.replace("px", ""))
  if (Number.isFinite(px)) {
    if (px <= 13) return "2"
    if (px <= 17) return "3"
    if (px <= 22) return "4"
    return "5"
  }
  if (cleaned.includes("small")) return "2"
  if (cleaned.includes("large") || cleaned.includes("x-large")) return "5"
  if (cleaned.includes("medium") || cleaned.includes("normal")) return "3"
  return ""
}

export function fontLabel(value: string) {
  const normalized = normalizeFontName(value)
  if (!normalized) return "默认字体"
  return normalized === "Microsoft YaHei" ? "微软雅黑" : normalized
}

export function fontSizeLabel(value: string) {
  const normalized = normalizeFontSize(value) || "3"
  return composerFontSizeOptions.find(([size]) => size === normalized)?.[1] || "正文"
}

export function normalizeInsertUrl(value: string, kind: InsertDialogState["kind"]) {
  const trimmed = value.trim()
  if (!trimmed) return ""
  const allowed =
    kind === "image" ? /^(https?:|cid:|data:image\/|\/)/i : /^(https?:|mailto:|tel:|#|\/)/i
  return allowed.test(trimmed) ? trimmed : `https://${trimmed}`
}

export function composerInitialHtml(defaultValue: string, defaultHtml?: string) {
  return (
    sanitizeComposerHtml(defaultHtml !== undefined ? defaultHtml : plainTextToHtml(defaultValue)) ||
    "<p></p>"
  )
}

export function composerValueFromEditor(editor: Editor): ComposerValue {
  const text = editor
    .getText({ blockSeparator: "\n" })
    .replace(/\u00a0/g, " ")
    .trimEnd()
  const html = sanitizeComposerHtml(editor.getHTML())
  if (!text.trim() && !htmlContainsMeaningfulContent(html)) return { text: "", html: "" }
  return { text, html: html || plainTextToHtml(text) }
}

export const QUOTED_MAIL_HEADER_RE =
  /-{2,}\s*(?:原始邮件|转发邮件|Original Message|Forwarded Message)\s*-{2,}/i

/**
 * 把「原始邮件」引用块从正文 HTML 里拆出来。
 *
 * 富文本编辑器（tiptap）的 schema 里没有 table/style，newsletter 那种表格排版的
 * 正文一旦放进编辑器就会被压成单列、颜色和列宽全丢。所以引用块干脆不进编辑器：
 * 自己写的正文照常编辑，引用块用只读 iframe 原样渲染，发信时再把原始 HTML
 * 原封不动拼回去。
 */
export function splitQuotedComposerHtml(html: string): {
  html: string
  quotedHtml: string
  quotedText: string
} {
  const raw = html || ""
  if (!raw.trim() || typeof window === "undefined") {
    return { html: raw, quotedHtml: "", quotedText: "" }
  }
  const doc = new DOMParser().parseFromString(`<body>${raw}</body>`, "text/html")
  const quotes = Array.from(doc.body.querySelectorAll("blockquote")).filter(
    (quote) =>
      !quote.parentElement?.closest("blockquote") &&
      QUOTED_MAIL_HEADER_RE.test(quote.textContent || "")
  )
  const quote = quotes[quotes.length - 1]
  if (!quote) return { html: raw, quotedHtml: "", quotedText: "" }
  const quotedHtml = quote.outerHTML
  const quotedText = (quote.textContent || "").replace(/\n{3,}/g, "\n\n").trim()
  quote.remove()
  return { html: doc.body.innerHTML, quotedHtml, quotedText }
}

export function editorTextSelection(editor: Editor) {
  const { from, to, empty } = editor.state.selection
  if (empty) return ""
  return editor.state.doc.textBetween(from, to, " ").trim()
}

export function selectedImageAttributes(editor: Editor) {
  const attrs = editor.getAttributes("image") as { src?: string; alt?: string }
  return attrs.src ? attrs : null
}
