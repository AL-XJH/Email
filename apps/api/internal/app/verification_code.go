package app

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// 验证码识别。
//
// 旧实现是「含关键词的那一行里找第一个 4-8 位数字」。遇到营销邮件就完蛋：
// NVIDIA 的新闻邮件整封正文是一行 2600 字的段落，里面有 "promo code PERPLEXITYRTX"，
// 也有 "GeForce 8800 GTX"，于是 8800 被当成验证码。
//
// 现在是「关键词后面一小段窗口内」取值：真验证码永远紧跟在关键词后面
// （验证码：857149 / code is 638204 / passcode 5521），营销文里的数字离关键词很远。
// 和前端 apps/web/src/lib/verification-code.ts 保持一致。
var (
	verificationKeywordPattern = regexp.MustCompile(`(?i)(验证码|校验码|动态码|安全码|确认码|verification\s*code|verify\s*code|passcode|one[\s-]?time\s*(?:code|password)|\botp\b|\bcode\b)`)
	verificationNumericPattern = regexp.MustCompile(`\b(\d{4,8})\b`)
	verificationMixedPattern   = regexp.MustCompile(`\b([0-9A-Za-z]{5,8})\b`)
)

const (
	// verificationScanLimit 只看正文前若干字符：验证码永远在开头几段，
	// 全文扫描对几万字的长邮件是纯浪费。
	verificationScanLimit = 8000
	// verificationWindow 关键词之后最多看这么多个字符。
	verificationWindow = 24
)

// URL 参数里的数字不算验证码（?code=8800 / /8800.html）。
const verificationGlueChars = "/=-_.%&?"

func gluedToURL(window string, index int) bool {
	if index <= 0 {
		return false
	}
	return strings.ContainsRune(verificationGlueChars, rune(window[index-1]))
}

func codeInWindow(window string) string {
	if loc := verificationNumericPattern.FindStringSubmatchIndex(window); loc != nil {
		if !gluedToURL(window, loc[2]) {
			return window[loc[2]:loc[3]]
		}
	}
	if loc := verificationMixedPattern.FindStringSubmatchIndex(window); loc != nil {
		code := window[loc[2]:loc[3]]
		if strings.ContainsAny(code, "0123456789") && !gluedToURL(window, loc[2]) {
			return code
		}
	}
	return ""
}

// 正文优先、主题兜底：主题里常出现日期、期号这类数字，
// 真正的验证码基本都在正文那句「验证码：xxxx」里。
func extractVerificationCode(subject, text string) string {
	body := strings.TrimSpace(text + "\n" + subject)
	if body == "" {
		return ""
	}
	if utf8.RuneCountInString(body) > verificationScanLimit {
		body = string([]rune(body)[:verificationScanLimit])
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if !strings.ContainsAny(line, "0123456789") {
			continue
		}
		for _, loc := range verificationKeywordPattern.FindAllStringIndex(line, -1) {
			rest := line[loc[1]:]
			if utf8.RuneCountInString(rest) > verificationWindow {
				rest = string([]rune(rest)[:verificationWindow])
			}
			if code := codeInWindow(rest); code != "" {
				return code
			}
		}
	}
	return ""
}
