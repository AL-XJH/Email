/**
 * 「要不要给这封邮件显示翻译按钮」的判断。
 *
 * 只看字母/汉字数量是不够的——纯文本邮件里一条追踪链接就有上千个字母：
 *   <https://c.gle/AGSJxQxHyhbP9QWg-81LooUkNYWws...>
 * Google 那封几乎全中文的通知，正文里塞了三条这种链接，latin 直接被撑到 1111、
 * 汉字才 122，于是被当成「英文邮件」推出了翻译按钮。
 *
 * 所以先把 URL、邮箱地址、base64/追踪令牌这些「不是人话的字母」去掉，再比较。
 */

const NON_PROSE_PATTERNS: RegExp[] = [
  /<https?:\/\/[^>\s]+>/gi, // 纯文本邮件里的 <https://...>
  /https?:\/\/\S+/gi, // 裸链接
  /\bwww\.\S+/gi,
  /\b[\w.+-]+@[\w.-]+\.[a-z]{2,}\b/gi, // 邮箱地址
  /\b[a-z0-9+/]{24,}={0,2}\b/gi, // base64 片段、追踪令牌
  /\bcid:[^\s"'<>]+/gi, // 内嵌图片引用
]

export const TRANSLATION_SAMPLE_CHARS = 6000

export function stripNonProse(value: string): string {
  let out = value
  for (const pattern of NON_PROSE_PATTERNS) {
    out = out.replace(pattern, " ")
  }
  return out
}

export function countProseLetters(value: string): { cjk: number; latin: number } {
  const prose = stripNonProse(value)
  const cjk = (prose.match(/[\u4e00-\u9fff]/g) || []).length
  const latin = (prose.match(/[A-Za-z]/g) || []).length
  return { cjk, latin }
}

/**
 * 界面语言是中文时，只有「去掉链接之后仍然有 80 个以上字母，且字母数超过汉字数的两倍」
 * 才认为这封邮件主要不是中文；英文界面反向判断。
 */
export function shouldOfferTranslation(text: string, language: string): boolean {
  const sample = text.trim().slice(0, TRANSLATION_SAMPLE_CHARS)
  if (!sample) return false
  const { cjk, latin } = countProseLetters(sample)
  if (language === "zh-CN" || language === "zh-TW") {
    return latin >= 80 && latin > cjk * 2
  }
  if (language === "en") {
    return cjk >= 20 && cjk > latin * 0.5
  }
  return false
}
