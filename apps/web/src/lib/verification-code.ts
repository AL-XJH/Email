// 验证码识别。
//
// 旧实现是「含关键词的那一行里找第一个 4-8 位数字」。遇到营销邮件就完蛋：
// NVIDIA 的新闻邮件整封正文是一行 2600 字的段落，里面有 "promo code PERPLEXITYRTX"，
// 也有 "GeForce 8800 GTX"，于是 8800 被当成验证码。
//
// 改成「关键词后面一小段窗口内」取值：真验证码永远紧跟在关键词后面
// （验证码：857149 / code is 638204 / passcode 5521），而营销文里的数字离关键词很远。
const VERIFICATION_KEYWORD =
  /(验证码|校验码|动态码|安全码|确认码|一次性(?:代码|密码|验证码|口令)|临时(?:代码|密码|验证码|口令)|登录(?:代码|密码)|认证代码|verification\s*code|verify\s*code|single[\s-]?use\s*code|security\s*code|login\s*code|sign[\s-]?in\s*code|authentication\s*code|passcode|one[\s-]?time\s*(?:code|password)|\botp\b|\bcode\b)/gi

// 有些服务把说明写成一句话，验证码放在句末：
// "Please enter the code to verify your account. 264248"
// 通用窗口只有 24 字，够不到句末数字；只在明确的
// enter ... code ... verify/account 结构里放宽窗口，避免营销邮件误判。
const VERIFICATION_INSTRUCTION =
  /\b(?:enter|input|type|use|submit)\b[^\n]{0,40}?\bcode\b[^\n]{0,80}?\b(?:verify|verification|confirm|account|login|sign[\s-]?in)\b[^\n]{0,40}?\b(\d{4,8})\b/i

// 关键词之后最多看这么多个字符
const VERIFICATION_WINDOW = 24

// URL 参数里的数字不算验证码（?code=8800 / /8800.html）
const GLUE_CHARS = "/=-_.%&?"

function gluedToUrl(window: string, index: number): boolean {
  return index > 0 && GLUE_CHARS.includes(window[index - 1])
}

function codeInWindow(window: string): string {
  const numeric = /\b(\d{4,8})\b/g
  let match: RegExpExecArray | null
  while ((match = numeric.exec(window))) {
    if (!gluedToUrl(window, match.index)) return match[1]
  }
  const mixed = /\b(?=[A-Za-z]*\d)([0-9A-Za-z]{5,8})\b/g
  while ((match = mixed.exec(window))) {
    if (!gluedToUrl(window, match.index)) return match[1]
  }
  return ""
}

// 正文优先、主题兜底：主题里常出现日期、期号这类数字，
// 真正的验证码基本都在正文那句「验证码：xxxx」里。
export function extractVerificationCode(subject: string, text: string): string {
  const body = `${text || ""}\n${subject || ""}`.trim()
  if (!body) return ""
  for (const line of body.split(/\r?\n/)) {
    if (!/\d/.test(line)) continue
    const instruction = line.match(VERIFICATION_INSTRUCTION)
    if (instruction) return instruction[1]
    VERIFICATION_KEYWORD.lastIndex = 0
    let keyword: RegExpExecArray | null
    while ((keyword = VERIFICATION_KEYWORD.exec(line))) {
      const start = keyword.index + keyword[0].length
      const window = line.slice(start, start + VERIFICATION_WINDOW)
      const code = codeInWindow(window)
      if (code) return code
    }
  }
  return ""
}
