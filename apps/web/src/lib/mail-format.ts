import type { MailMessage } from "@/lib/api-types"
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
