package app

import (
	"strings"

	"golang.org/x/net/html"
)

// htmlToPlainText 把邮件 HTML 转成可读纯文本：保留段落、列表和换行，
// 丢掉脚本、样式和头部元数据。
//
// 直接调用 util.go 的 stripTags 会把整封邮件压成一行——TG 推送要「发全文」时
// 那种一坨的文本没法读，所以这里单独按块级元素补换行。
func htmlToPlainText(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return strings.Join(strings.Fields(stripTags(source)), " ")
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "head", "title", "noscript", "template", "svg":
				return
			case "br":
				b.WriteByte('\n')
				return
			case "li":
				b.WriteByte('\n')
				b.WriteString("• ")
			case "p", "div", "tr", "blockquote", "section", "article", "table", "ul", "ol",
				"h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(html.UnescapeString(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "p", "div", "tr", "blockquote", "section", "article", "table", "li",
				"h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteByte('\n')
			}
		}
	}
	walk(doc)
	return normalizePlainBody(b.String())
}

// normalizePlainBody 压掉多余空白：行尾空格、连续空行、行内连续空格。
func normalizePlainBody(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		// 列表项之间不留空行：• 开头的行如果前面是空行，把空行去掉
		if strings.HasPrefix(line, "• ") && len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// emailPlainBody 取邮件正文的纯文本。
//
// 优先 text/plain，但有个例外：只有 HTML 的邮件入库时 body_text 是「剥标签压成一行」
// 的产物（没有换行、段落全糊在一起），这时候用 HTML 重新转一遍可读性好得多。
func emailPlainBody(bodyText, bodyHTML, snippet string) string {
	text := normalizePlainBody(bodyText)
	rich := htmlToPlainText(bodyHTML)
	switch {
	case rich == "":
		if text != "" {
			return text
		}
		return normalizePlainBody(snippet)
	case text == "":
		return rich
	case !strings.Contains(text, "\n") && strings.Count(rich, "\n") >= 2:
		// 纯文本是一整行、HTML 转出来有段落 → 说明这段文字是从 HTML 剥出来的
		return rich
	default:
		return text
	}
}
