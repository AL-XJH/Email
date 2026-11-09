import * as React from "react"

export type Language = "zh-CN" | "zh-TW" | "en"

export const LANGUAGE_STORAGE_KEY = "lanqin:language"

export const languageOptions: {
  value: Language
  label: string
  shortLabel: string
  htmlLang: string
}[] = [
  { value: "zh-CN", label: "简体中文", shortLabel: "简", htmlLang: "zh-CN" },
  { value: "zh-TW", label: "繁體中文", shortLabel: "繁", htmlLang: "zh-TW" },
  { value: "en", label: "English", shortLabel: "EN", htmlLang: "en" },
]

/**
 * 界面锁定为简体中文：不再根据浏览器语言或本地存储切换。
 * 多语言文案表仍然保留（历史遗留的 zh-TW / en 词条不会再被选中）。
 */
export const LOCKED_LANGUAGE: Language = "zh-CN"

if (typeof window !== "undefined") {
  // 清掉历史版本可能写入的语言偏好，避免旧浏览器残留 en / zh-TW。
  try {
    window.localStorage.removeItem(LANGUAGE_STORAGE_KEY)
  } catch {
    /* Storage may be unavailable. */
  }
}

export function getInitialLanguage(): Language {
  // 运行时等价于 LOCKED_LANGUAGE（启动时本地存储里的语言偏好已被清掉）；
  // 单测会显式切换 activeLanguage 来校验三套词条。
  return activeLanguage
}

// 运行时界面语言永远是 LOCKED_LANGUAGE；这个模块变量只服务于
// 文案表自身（i18n 单测会显式切换它来校验三种语言的词条）。
function initialActiveLanguage(): Language {
  if (typeof window === "undefined") return LOCKED_LANGUAGE
  try {
    const stored = window.localStorage.getItem(LANGUAGE_STORAGE_KEY)
    if (stored === "zh-CN" || stored === "zh-TW" || stored === "en") return stored
  } catch {
    /* Storage may be unavailable. */
  }
  return LOCKED_LANGUAGE
}

let activeLanguage: Language = initialActiveLanguage()

export function setStoredLanguage(language: Language) {
  activeLanguage = language
  if (typeof window === "undefined") return
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, language)
  } catch {
    /* Storage may be unavailable. */
  }
  try {
    window.dispatchEvent(new CustomEvent("lanqin:language", { detail: language }))
  } catch {
    /* Event dispatch is best-effort. */
  }
}

export function useLanguage() {
  // 语言已锁定，不再订阅 storage / lanqin:language 事件。
  const [language] = React.useState<Language>(() => LOCKED_LANGUAGE)
  const setLanguage = React.useCallback((_nextLanguage: Language) => {
    /* 语言已锁定，忽略切换请求。 */
  }, [])
  return [language, setLanguage] as const
}

// 界面语言锁定简体中文，历史的三语言词条表（约 127KB 死数据）已整体移除。
// translateUiText 仍被大量调用点使用，保留成「原样返回」的薄封装。
export function translateUiText(value: string, _language: Language): string {
  return value
}

// UI messages retain their source and arguments until render, so open notices
// follow language changes without translating user-provided parameter values.
export type UiMessage = {
  key: string
  values?: readonly UiValue[]
  lines?: readonly UiText[]
}
export type UiText = string | UiMessage
export type UiValue = string | number | undefined | null | { date: string } | UiMessage

export function uiMessage(key: string, values?: readonly UiValue[]): UiMessage {
  return { key, values }
}

export function uiText(
  value: UiText,
  values?: readonly UiValue[],
  language = activeLanguage
): string {
  if (typeof value !== "string" && value.lines)
    return value.lines.map((line) => uiText(line, undefined, language)).join("\n")
  const source = typeof value === "string" ? value : value.key
  const parameters = typeof value === "string" ? values : value.values
  const translated = translateUiText(source, language)
  return parameters
    ? translated.replace(/\{(\d+)\}/g, (match, index: string) => {
        if (!(Number(index) in parameters)) return match
        const parameter = parameters[Number(index)]
        if (typeof parameter === "number") return new Intl.NumberFormat(language).format(parameter)
        if (parameter && typeof parameter === "object") {
          if ("key" in parameter) return uiText(parameter, undefined, language)
          const date = new Date(parameter.date)
          return Number.isNaN(date.getTime())
            ? ""
            : new Intl.DateTimeFormat(language, {
                year: "numeric",
                month: "2-digit",
                day: "2-digit",
                hour: "2-digit",
                minute: "2-digit",
              }).format(date)
        }
        return String(parameter ?? "")
      })
    : translated
}

export function LanguageDomSync() {
  const [language] = useLanguage()
  React.useEffect(() => {
    document.documentElement.lang = language
  }, [language])
  return null
}

export function UiLabel({ text }: { text: UiText }) {
  const [language] = useLanguage()
  return <>{uiText(text, undefined, language)}</>
}
