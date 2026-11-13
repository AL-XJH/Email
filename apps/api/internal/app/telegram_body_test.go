package app

import (
	"regexp"
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
	for _, want := range []string{"维护公告", "香港区域将于今晚维护。", "预计 30 分钟。", "• 节点 A\n• 节点 B"} {
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
	// body_text 只有一行、HTML 有段落时，用 HTML 转出来的版本
	flat := "HTML 邮件推送测试 这是第一段 这是第二段 列表一列表二"
	rich := "<h2>HTML 邮件推送测试</h2><p>这是第一段</p><p>这是第二段</p><ul><li>列表一</li><li>列表二</li></ul>"
	if got := emailPlainBody(flat, rich, ""); !strings.Contains(got, "\n") {
		t.Fatalf("剥标签来的单行正文应该改用 HTML 版本（要有换行），实际 %q", got)
	}
}

func TestTelegramMessagePartsShortBodySingleMessage(t *testing.T) {
	parts := telegramMessageParts("admin@example.test", "ChatGPT <noreply@example.test>", "您的临时验证码", "857149",
		"输入此临时验证码以继续：857149", 0)
	if len(parts) != 1 {
		t.Fatalf("短邮件不该分段，实际 %d 段", len(parts))
	}
	lines := strings.Split(parts[0], "\n")
	// 第一行是加粗的主题，验证码仍旧占第二行（推送预览只露前两行）。
	if lines[0] != "<b>您的临时验证码</b>" {
		t.Fatalf("第一行应该是加粗主题，实际 %q", lines[0])
	}
	if lines[1] != "验证码: 857149" {
		t.Fatalf("验证码必须在第二行，实际:\n%s", parts[0])
	}
	if !strings.Contains(parts[0], "输入此临时验证码以继续：857149") {
		t.Fatalf("正文没进推送:\n%s", parts[0])
	}
	if strings.HasPrefix(parts[0], "(1/") {
		t.Fatalf("单条消息不该带分段前缀:\n%s", parts[0])
	}
}

func TestTelegramMessagePartsRespectsBudget(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 400; i++ {
		body.WriteString("这是一段用于测试的正文内容，长度大约二十个字。\n")
	}
	parts := telegramMessageParts("admin@example.test", "sender@example.test", "长邮件", "", body.String(), 0)
	if len(parts) != 1 {
		t.Fatalf("现在只发一条，实际 %d 条", len(parts))
	}
	if n := len([]rune(parts[0])); n > telegramNotificationTextBudget+40 {
		t.Fatalf("推送 %d 字，超过预算 %d", n, telegramNotificationTextBudget)
	}
	if !strings.Contains(parts[0], "已截断，完整内容请点「打开邮件」") {
		t.Fatalf("超长邮件应提示去网页看全文:\n%.160q", parts[0])
	}
	if !strings.HasPrefix(parts[0], "<b>长邮件</b>\n") {
		t.Fatalf("消息头第一行应该是加粗主题:\n%.120q", parts[0])
	}
}

func TestTelegramMessagePartsTruncatesAbsurdBody(t *testing.T) {
	body := strings.Repeat("很长的正文。", 8000) // 48000 字符
	parts := telegramMessageParts("m@example.test", "s@example.test", "超长", "", body, 0)
	if len(parts) != 1 {
		t.Fatalf("截断后仍是一条，实际 %d 条", len(parts))
	}
	if n := len([]rune(parts[0])); n > telegramNotificationTextBudget+40 {
		t.Fatalf("推送 %d 字，超过预算 %d", n, telegramNotificationTextBudget)
	}
	if !strings.Contains(parts[0], "已截断") {
		t.Fatalf("超长正文应提示已截断:\n%.120q", parts[0][len(parts[0])-80:])
	}
}

func TestTelegramMessagePartsMakesLongLinksClickable(t *testing.T) {
	long := "https://hs-41051298.t.hubspotemail.net/e2t/c/" + strings.Repeat("a", 200)
	short := "https://cerebras.ai/blog"
	parts := telegramMessageParts("m@example.test", "s@example.test", "链接测试", "",
		"看这里 "+long+" 还有 "+short+" 和 <"+long+">", 0)
	if len(parts) != 1 {
		t.Fatalf("这么短的正文应该一条发完，实际 %d 条", len(parts))
	}
	got := parts[0]
	if !strings.Contains(got, `<a href="`+long+`">链接</a>`) {
		t.Fatalf("长链接应变成可点的「链接」:\n%s", got)
	}
	if strings.Count(got, ">链接</a>") != 2 {
		t.Fatalf("两条长链接（含 <...> 写法的）都应变成「链接」，实际:\n%s", got)
	}
	if !strings.Contains(got, short) {
		t.Fatalf("短链接应保持原样:\n%s", got)
	}
	// 可见文字里不能残留那串 a（只允许出现在 href 里）
	visible := regexp.MustCompile(`<a href="[^"]*">链接</a>`).ReplaceAllString(got, "")
	if strings.Contains(visible, strings.Repeat("a", 50)) {
		t.Fatalf("可见文本里还留着长链接原文:\n%s", visible)
	}
}

func TestTelegramMessagePartsEscapesTextButNotHref(t *testing.T) {
	long := "https://example.com/track/" + strings.Repeat("b", 80) + "?a=1&b=2"
	parts := telegramMessageParts("m@example.test", "S & T <ops@example.test>", "A & B <C>", "",
		"A & B < C "+long, 0)
	got := parts[0]
	if !strings.Contains(got, "A &amp; B &lt; C") {
		t.Fatalf("正文里的 & 和 < 应被转义:\n%s", got)
	}
	if !strings.Contains(got, "S &amp; T &lt;ops@example.test&gt;") {
		t.Fatalf("消息头里的 & 和 < 也应被转义:\n%s", got)
	}
	if !strings.Contains(got, "?a=1&amp;b=2") {
		t.Fatalf("href 里的 & 要转成 &amp;，否则 Telegram 解析失败:\n%s", got)
	}
}

func TestTelegramMessagePartsCollapsesLinkOnlyNewsletter(t *testing.T) {
	// 模拟那封 Cerebras 邮件：正文 9758 字，其中 82% 是 17 条超长追踪链接
	var body strings.Builder
	body.WriteString("Cerebras speed is coming to Oracle Cloud.\n\n")
	for i := 0; i < 17; i++ {
		body.WriteString("Read more: https://hs-41051298.t.hubspotemail.net/e2t/c/" + strings.Repeat("x", 460) + "\n\n")
		body.WriteString("Cerebras and Oracle: the world's fastest AI is coming to Oracle Cloud Infrastructure.\n\n")
	}
	parts := telegramMessageParts("user@example.test", "Cerebras <info@cerebras.net>", "The Latest from Cerebras", "", body.String(), 20000)
	if len(parts) != 1 {
		t.Fatalf("压掉追踪链接后应该只剩一条，实际 %d 条（共 %d 字符）", len(parts), len([]rune(strings.Join(parts, ""))))
	}
	if !strings.Contains(parts[0], ">链接</a>") {
		t.Fatalf("没有把长链接压成可点的「链接」")
	}
	if strings.Count(parts[0], ">链接</a>") != 17 {
		t.Fatalf("17 条链接应该都在，实际 %d 条", strings.Count(parts[0], ">链接</a>"))
	}
}

func TestEmailPlainBodyPrefersHTMLWhenPlainTextIsCSS(t *testing.T) {
	plain := `body { margin: 0; padding: 0; width: 100% !important; }
@media screen and (max-width: 600px) { .wrap { width: 100% !important; } }
Outstanding invoice OD-7K2M9R4Q — $84.00 not yet paid.`
	html := `<html><body><p>Outstanding invoice OD-7K2M9R4Q — $84.00 not yet paid.</p><p>Complete your renewal payment.</p></body></html>`
	got := emailPlainBody(plain, html, "")
	if strings.Contains(got, "@media") || strings.Contains(got, "!important") {
		t.Fatalf("CSS leaked into plain body: %.200q", got)
	}
	if !strings.Contains(got, "Outstanding invoice") || !strings.Contains(got, "Complete your renewal payment") {
		t.Fatalf("HTML body was not used: %.300q", got)
	}
	if snippet := snippetFrom(got, html); strings.Contains(snippet, "@media") {
		t.Fatalf("CSS leaked into snippet: %q", snippet)
	}
}
