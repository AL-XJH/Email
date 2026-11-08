package app

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	// 推送正文最多翻这么多字：最终会截到 1300 字，翻到尾巴上纯属浪费上游配额和时间。
	telegramTranslateMaxRunes = 2400
	// 翻译最多等这么久；超时就发原文，不能把推送队列堵死。
	telegramTranslateTimeout = 15 * time.Second
)

var (
	telegramURLPattern   = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	telegramEmailPattern = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	telegramAnglePattern = regexp.MustCompile(`<[^>\s]*>`)
)

// telegramProseCounts 数正文里的字母和汉字。
// 链接、邮箱、<...> 占位先剔掉，否则一封中文邮件里几条追踪链接就能把字母数撑到上千，
// 中文邮件也会被误判成外文（网页端的翻译按钮就踩过这个坑）。
func telegramProseCounts(text string) (latin, cjk int) {
	cleaned := telegramAnglePattern.ReplaceAllString(text, " ")
	cleaned = telegramURLPattern.ReplaceAllString(cleaned, " ")
	cleaned = telegramEmailPattern.ReplaceAllString(cleaned, " ")
	for _, r := range cleaned {
		switch {
		case unicode.Is(unicode.Latin, r):
			latin++
		case unicode.Is(unicode.Han, r):
			cjk++
		}
	}
	return latin, cjk
}

// telegramNeedsTranslation 判断一段文字是否需要翻成中文：
// 字母够多、且明显多过汉字。纯中文、中英混排的中文邮件都不动。
func telegramNeedsTranslation(text string, minLatin int) bool {
	latin, cjk := telegramProseCounts(text)
	if latin < minLatin {
		return false
	}
	if cjk == 0 {
		return true
	}
	return latin > cjk*2
}

// translateTelegramPayload 在真正发送前把外文邮件翻成中文，再重新拼一次消息。
//
// 放在 worker 而不是收信时做：翻译要打外部接口，慢的时候几秒，
// 不能卡住投递；而且失败了还能靠 outbox 重试，重试时缓存里已有译文。
// 翻译不通就原样发原文，绝不因为翻译把推送弄丢。
func (a *App) translateTelegramPayload(ctx context.Context, payload telegramNotificationPayload) telegramNotificationPayload {
	body := strings.TrimSpace(payload.Body)
	if body == "" {
		return payload
	}
	needBody := telegramNeedsTranslation(body, 40)
	needSubject := telegramNeedsTranslation(payload.Subject, 8)
	if !needBody && !needSubject {
		return payload
	}
	if clipped, cut := truncateRunes(body, telegramTranslateMaxRunes); cut {
		body = clipped
	}
	texts := []string{"", body}
	if needSubject {
		texts[0] = payload.Subject
	}
	tctx, cancel := context.WithTimeout(ctx, telegramTranslateTimeout)
	defer cancel()
	out, _, err := a.translateTexts(tctx, texts, "zh-CN")
	changed := false
	if needSubject && len(out) > 0 && strings.TrimSpace(out[0]) != "" && out[0] != payload.Subject {
		payload.Subject = out[0]
		changed = true
	}
	if len(out) > 1 && strings.TrimSpace(out[1]) != "" && out[1] != body {
		payload.Body = out[1]
		changed = true
	}
	if err != nil || !changed {
		// 一个字都没翻出来（或翻译结果和原文一样）就按原文发。
		return payload
	}
	// 一行日志方便排查「这条推送到底翻没翻」，不打印任何正文内容。
	if a.log != nil {
		a.log.Info("Telegram notification translated before sending", "target", "zh-CN")
	}
	parts := telegramMessageParts(payload.Mailbox, payload.From, payload.Subject, payload.Code, payload.Body, telegramNotificationTextBudget)
	if len(parts) > 0 {
		payload.Text = parts[0]
		payload.Parts = parts
	}
	return payload
}
