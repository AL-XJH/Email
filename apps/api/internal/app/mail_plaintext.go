package app

import (
	"fmt"
	"regexp"
	"strconv"
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

var styleSheetSignalPattern = regexp.MustCompile(`(?i)(@media\s|!important|font-family\s*:|text-decoration\s*:|background-color\s*:|border-collapse\s*:)`)

// looksLikeStyleSheet 判断一段 text/plain 是否其实是 HTML 里的 CSS。
// 有些发件系统会把 <style> 内容塞进 text/plain 部件；直接用这段做摘要，
// 列表和 Telegram 推送就会变成 "a:hover { ... } @media ..."。
func looksLikeStyleSheet(value string) bool {
	if !strings.ContainsAny(value, "{}") {
		return false
	}
	matches := styleSheetSignalPattern.FindAllString(value, -1)
	return len(matches) >= 2 || strings.Contains(strings.ToLower(value), "@media")
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
	if rich != "" && looksLikeStyleSheet(text) {
		// text/plain 是 CSS 垃圾时，HTML 版本才是真正正文。
		return rich
	}
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

// 超长 URL（追踪链接、带一大堆参数的营销链接）在聊天里就是一大坨蓝色文字，
// 既占篇幅又没法读。短的保持原样（点了能跳），长的收成可点的两个字「链接」。
var (
	plainBracketLongLinkRe    = regexp.MustCompile(`<(https?://\S{60,})>`)
	plainLongLinkRe           = regexp.MustCompile(`https?://\S{60,}`)
	telegramLinkPlaceholderRe = regexp.MustCompile("\x01L[0-9]+\x01")
)

// telegramEscapeText 转义 Telegram HTML 模式里有含义的字符。
func telegramEscapeText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

func telegramEscapeAttr(value string) string {
	return strings.ReplaceAll(telegramEscapeText(value), `"`, "&quot;")
}

// protectLongLinks 把超长链接换成占位符，返回替换后的文本和链接清单。
//
// 必须先替换再分段：一条 HubSpot 追踪链接就有四五百字符，不先收起来的话，
// 分段会按「原始长度」把一封其实只有两千字的邮件拆成三条。
func protectLongLinks(body string) (string, []string) {
	links := []string{}
	trimmed := plainBracketLongLinkRe.ReplaceAllString(body, "$1")
	text := plainLongLinkRe.ReplaceAllStringFunc(trimmed, func(match string) string {
		links = append(links, match)
		return fmt.Sprintf("\x01L%d\x01", len(links)-1)
	})
	return text, links
}

// telegramChunkHTML 把一段（已占位化的）文本转成 Telegram HTML：
// 占位符换成可点的「链接」锚点，其余文字全部转义。
func telegramChunkHTML(chunk string, links []string) string {
	escaped := telegramEscapeText(chunk)
	return telegramLinkPlaceholderRe.ReplaceAllStringFunc(escaped, func(match string) string {
		idx, err := strconv.Atoi(strings.Trim(match, "\x01L"))
		if err != nil || idx < 0 || idx >= len(links) {
			return "链接"
		}
		return `<a href="` + telegramEscapeAttr(links[idx]) + `">链接</a>`
	})
}
