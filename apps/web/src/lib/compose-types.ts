import type { UiText } from "@/lib/language"

export type ComposeDraft = {
  key: string
  id?: string
  mailboxId?: string
  to?: string
  cc?: string
  bcc?: string
  subject?: string
  text?: string
  html?: string
  files?: File[]
  isDraft?: boolean
}

export type ComposeSendIntent = {
  title: string
  description: UiText
  confirmText: string
  onConfirm: () => void
}
