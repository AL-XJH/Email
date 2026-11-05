package app

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// 验证码提取：只在「含关键词的那一行」里找数字，避免把订单号、车次号当成验证码。
// 和前端 apps/web/src/pages/mail.tsx 的 extractVerificationCode 保持一致。
var (
	verificationKeywordPattern = regexp.MustCompile(`(?i)(验证码|校验码|动态码|安全码|确认码|verification\s*code|verify\s*code|passcode|one[\s-]?time\s*(?:code|password)|\botp\b|\bcode\b)`)
	verificationNumericPattern = regexp.MustCompile(`\b(\d{4,8})\b`)
	verificationMixedPattern   = regexp.MustCompile(`\b([0-9A-Za-z]{5,8})\b`)
)

// verificationScanLimit 只看正文前若干字符：验证码永远在开头几段，
// 全文扫描对几万字的长邮件是纯浪费。
const verificationScanLimit = 8000

// 正文优先、主题兜底：主题里常出现日期、订单号这类数字，
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
		if !verificationKeywordPattern.MatchString(line) {
			continue
		}
		if match := verificationNumericPattern.FindStringSubmatch(line); match != nil {
			return match[1]
		}
		if match := verificationMixedPattern.FindStringSubmatch(line); match != nil {
			if strings.ContainsAny(match[1], "0123456789") {
				return match[1]
			}
		}
	}
	return ""
}
