import DOMPurify from "dompurify"

import type { MailMessage } from "@/lib/api-types"
import type { Language } from "@/lib/language"
import { decodeMimeHeader, generateLabelColor } from "@/lib/utils"

// 标签颜色优先用服务端存的值（seek.li 就是这么做的），缺失时才按名字哈希兜底。
export function labelDotColor(label: { name: string; color?: string | null }): string {
  return label.color || generateLabelColor(label.name).backgroundColor
}

export function senderDisplayName(message: MailMessage) {
  const fromName = decodeMimeHeader(message.fromName?.trim() || "")
  if (fromName) return fromName
  return displayNameFromAddress(message.from)
}

function displayNameFromAddress(value: string) {
  const text = decodeMimeHeader(value.trim())
  const namedAddress = text.match(/^"?([^"<]+?)"?\s*<[^>]+>$/)
  const name = namedAddress?.[1]?.trim()
  if (name) return name
  const address = text.match(/<([^>]+)>/)?.[1]?.trim() || text
  const localPart = address.split("@")[0]?.trim()
  return localPart || text || "未知发件人"
}

export function senderTitle(message: MailMessage) {
  const name = decodeMimeHeader(message.fromName?.trim() || "")
  const from = decodeMimeHeader(message.from)
  return name ? `${name} <${from}>` : from
}

export function attachmentHref(message: MailMessage, attachmentId: string) {
  if (message.externalAccountId) {
    return `/api/mail/external-accounts/${encodeURIComponent(message.externalAccountId)}/attachments/${encodeURIComponent(message.id)}/${encodeURIComponent(attachmentId)}`
  }
  return `/api/mail/attachments/${attachmentId}`
}

export function normalizeTranslationLanguage(language: Language) {
  if (language === "zh-CN") return "zh-CN"
  if (language === "zh-TW") return "zh-TW"
  if (language === "en") return "en"
  return ""
}

export function translationTargetLabel(language: Language) {
  if (language === "zh-CN") return "简体中文"
  if (language === "zh-TW") return "繁體中文"
  return "English"
}

export function cleanAccountName(name: string, email?: string) {
  const value = name.trim()
  if (!value || (email && value.toLowerCase() === email.toLowerCase()))
    return email?.split("@")[0] || "用户"
  return value
}

export function accountInitial(name: string, email?: string) {
  const source = cleanAccountName(name, email)
  const first = Array.from(source.trim())[0]
  return (first || "蓝").toUpperCase()
}

export function stripHtml(html: string) {
  const div = document.createElement("div")
  div.innerHTML = DOMPurify.sanitize(html)
  return div.textContent || div.innerText || ""
}

export function senderAddress(message: MailMessage) {
  return extractAddress(message.from) || extractAddress(message.fromName || "")
}

export function extractAddress(value?: string) {
  const text = decodeMimeHeader((value || "").trim())
  if (!text) return ""
  const bracketAddress = text.match(/<([^>]+)>/)?.[1]?.trim()
  return bracketAddress || text
}
