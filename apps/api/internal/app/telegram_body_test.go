package app

import (
	"strings"
	"testing"
)

func TestHTMLToPlainTextKeepsParagraphs(t *testing.T) {
	html := `<html><head><style>p{color:red}</style><title>标题</title></head><body>
		<h2>维护公告</h2>
		<p>香港区域将于今晚维护。</p>
		<p>预计 30 分钟。</p>
		<ul><li>节点 A</li><li>节点 B</li></ul>
		<script>alert(1)</script>
	</body></html>`
	got := htmlToPlainText(html)
	for _, want := range []string{"维护公告", "香港区域将于今晚维护。", "预计 30 分钟。", "• 节点 A", "• 节点 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("纯文本里缺少 %q，实际:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"color:red", "alert(1)", "标题", "<h2>"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("纯文本里不该出现 %q:\n%s", unwanted, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("连续空行没有收敛:\n%q", got)
	}
}

func TestEmailPlainBodyPrefersTextPart(t *testing.T) {
	if got := emailPlainBody("正文纯文本", "<p>另一个版本</p>", "摘要"); got != "正文纯文本" {
		t.Fatalf("应优先用 text/plain，实际 %q", got)
	}
	if got := emailPlainBody("", "<p>只有 HTML</p>", "摘要"); got != "只有 HTML" {
		t.Fatalf("没有纯文本时应从 HTML 转，实际 %q", got)
	}
	if got := emailPlainBody("  ", "", "摘要兜底"); got != "摘要兜底" {
		t.Fatalf("两个正文都空时应退回摘要，实际 %q", got)
	}
}

func TestTelegramMessagePartsShortBodySingleMessage(t *testing.T) {
	parts := telegramMessageParts("admin@example.test", "ChatGPT <noreply@example.test>", "您的临时验证码", "857149",
		"输入此临时验证码以继续：857149", telegramMessageMaxRunes)
	if len(parts) != 1 {
		t.Fatalf("短邮件不该分段，实际 %d 段", len(parts))
	}
	lines := strings.Split(parts[0], "\n")
	if lines[0] != "新邮件" || lines[1] != "验证码: 857149" {
		t.Fatalf("验证码必须在第二行，实际:\n%s", parts[0])
	}
	if !strings.Contains(parts[0], "输入此临时验证码以继续：857149") {
		t.Fatalf("正文没进推送:\n%s", parts[0])
	}
	if strings.HasPrefix(parts[0], "(1/") {
		t.Fatalf("单条消息不该带分段前缀:\n%s", parts[0])
	}
}

func TestTelegramMessagePartsSplitsLongBody(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 400; i++ {
		body.WriteString("这是一段用于测试的正文内容，长度大约二十个字。\n")
	}
	parts := telegramMessageParts("admin@example.test", "sender@example.test", "长邮件", "", body.String(), telegramMessageMaxRunes)
	if len(parts) < 2 {
		t.Fatalf("长正文应该分段，实际 %d 段", len(parts))
	}
	for i, part := range parts {
		if n := len([]rune(part)); n > telegramMessageMaxRunes {
			t.Fatalf("第 %d 段 %d 字符，超过单条上限 %d", i+1, n, telegramMessageMaxRunes)
		}
		prefix := "(" + itoa(i+1) + "/" + itoa(len(parts)) + ")"
		if !strings.HasPrefix(part, prefix) {
			t.Fatalf("第 %d 段缺少前缀 %q: %.40q", i+1, prefix, part)
		}
	}
	if !strings.Contains(parts[0], "主题: 长邮件") {
		t.Fatalf("消息头只应出现在第一段:\n%.120q", parts[0])
	}
	if strings.Contains(parts[1], "主题: 长邮件") {
		t.Fatalf("第二段不该重复消息头:\n%.120q", parts[1])
	}
	// 拼回去应当还是 400 行原文（分段只加前缀和消息头，不丢内容）
	joined := strings.Join(parts, "")
	if got := strings.Count(joined, "这是一段用于测试的正文内容"); got != 400 {
		t.Fatalf("分段过程丢了正文，实际 %d 行（应为 400）", got)
	}
}

func TestTelegramMessagePartsTruncatesAbsurdBody(t *testing.T) {
	body := strings.Repeat("很长的正文。", 8000) // 48000 字符
	parts := telegramMessageParts("m@example.test", "s@example.test", "超长", "", body, telegramMessageMaxRunes)
	last := parts[len(parts)-1]
	if !strings.Contains(last, "正文过长，已截断") {
		t.Fatalf("超长正文应提示已截断，实际结尾:\n%.120q", last)
	}
	if len(parts) > 8 {
		t.Fatalf("截断后仍拆出 %d 段，太多了", len(parts))
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
