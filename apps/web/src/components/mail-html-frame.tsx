import * as React from "react"
import { buildMailFrameSrcDoc } from "@/components/mail-content"
import { translateUiText, uiText, useLanguage } from "@/lib/language"

/**
 * Renders untrusted mail HTML inside a sandboxed iframe.
 *
 * Both the webmail reader and the admin message viewer must use this. Sanitising
 * with DOMPurify alone is not enough: its default allow-list keeps `style`
 * attributes and `<style>` blocks, so a message could position an invisible overlay
 * over the surrounding application chrome. Rendering into an iframe without
 * `allow-scripts` confines both scripts and CSS to the frame.
 */
function stripRemoteContent(html: string): { html: string; count: number } {
  if (!html.trim()) return { html: "", count: 0 }
  const doc = new DOMParser().parseFromString(html, "text/html")
  let count = 0
  const isRemote = (value: string) => {
    const trimmed = value.trim()
    return trimmed !== "" && !/^data:/i.test(trimmed) && !/^cid:/i.test(trimmed)
  }
  doc.querySelectorAll("img").forEach((image) => {
    for (const attr of ["src", "srcset"]) {
      const value = image.getAttribute(attr)
      if (value && isRemote(value)) {
        image.removeAttribute(attr)
        count++
      }
    }
  })
  doc.querySelectorAll("[background]").forEach((element) => {
    const value = element.getAttribute("background") || ""
    if (isRemote(value)) {
      element.removeAttribute("background")
      count++
    }
  })
  doc.querySelectorAll("[style]").forEach((element) => {
    const style = element.getAttribute("style") || ""
    if (/(?:url|image-set)\s*\(/i.test(style)) {
      element.setAttribute("style", style.replace(/(?:url|image-set)\s*\([^)]*\)/gi, ""))
      count++
    }
  })
  doc.querySelectorAll("style").forEach((style) => {
    const css = style.textContent || ""
    if (/(?:url|image-set)\s*\(|@import/i.test(css)) {
      style.textContent = css
        .replace(/(?:url|image-set)\s*\([^)]*\)/gi, "")
        .replace(/@import[^;]+;/gi, "")
      count++
    }
  })
  return { html: doc.documentElement.outerHTML, count }
}

// 正则清不掉所有 CSS 写法（image-set、嵌套函数等），再加一层 CSP 兜底：
// 即使有漏网的 URL，sandbox 里的浏览器也不会去请求。
function blockRemoteCSP(html: string) {
  const meta =
    "<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'; font-src data:;\">"
  if (/<head[^>]*>/i.test(html)) return html.replace(/<head([^>]*)>/i, `<head$1>${meta}`)
  return meta + html
}

export function MailHtmlFrame({
  bodyHtml,
  bodyText,
  className,
  minHeight = 180,
}: {
  bodyHtml?: string
  bodyText?: string
  className?: string
  minHeight?: number
}) {
  const [language] = useLanguage()
  const iframeRef = React.useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = React.useState(260)
  const [showRemote, setShowRemote] = React.useState(false)
  const blocked = React.useMemo(() => stripRemoteContent(bodyHtml || ""), [bodyHtml])
  React.useEffect(() => setShowRemote(false), [bodyHtml])
  const activeHtml = showRemote ? bodyHtml || "" : blocked.html
  const srcDoc = React.useMemo(() => {
    const built = buildMailFrameSrcDoc(activeHtml, bodyText || "", language)
    return !showRemote && blocked.count > 0 ? blockRemoteCSP(built) : built
  }, [activeHtml, blocked.count, bodyText, language, showRemote])

  const resize = React.useCallback(() => {
    const frame = iframeRef.current
    const doc = frame?.contentDocument
    if (!frame || !doc) return
    const body = doc.body
    const html = doc.documentElement
    // Measure without the previous viewport height, so collapsing a quote can
    // shrink the frame again instead of leaving a large empty area.
    frame.style.height = "0px"
    const nextHeight = Math.max(
      minHeight,
      Math.ceil(
        Math.max(
          body?.scrollHeight || 0,
          body?.offsetHeight || 0,
          html?.scrollHeight || 0,
          html?.offsetHeight || 0
        )
      )
    )
    frame.style.height = `${nextHeight}px`
    setHeight(nextHeight)
  }, [minHeight])

  React.useEffect(() => {
    setHeight(260)
    const frame = iframeRef.current
    if (!frame) return
    let observer: ResizeObserver | undefined
    let observedDocument: Document | null = null
    const timers = [
      window.setTimeout(resize, 0),
      window.setTimeout(resize, 120),
      window.setTimeout(resize, 600),
    ]
    // 邮件里的「中文 / English」切换按钮是 <a href="#zh">。frame 的高度等于内容高度，
    // 它自己没得滚，所以要把父页面的滚动容器滚到锚点位置，用户才看得到效果。
    const handleFragmentClick = (event: Event) => {
      const target = event.target as Element | null
      const anchor = target?.closest?.('a[href^="#"]') as HTMLAnchorElement | null
      if (!anchor) return
      const frame = iframeRef.current
      const doc = anchor.ownerDocument
      const id = decodeURIComponent((anchor.getAttribute("href") || "").slice(1))
      const destination = id ? doc.getElementById(id) : null
      event.preventDefault()
      if (!frame || !destination) return
      const offset = frame.getBoundingClientRect().top + destination.getBoundingClientRect().top
      let scroller: HTMLElement | null = frame.parentElement
      while (scroller) {
        const style = window.getComputedStyle(scroller)
        if (
          /(auto|scroll|overlay)/.test(style.overflowY) &&
          scroller.scrollHeight > scroller.clientHeight
        ) {
          break
        }
        scroller = scroller.parentElement
      }
      if (scroller) {
        scroller.scrollBy({
          top: offset - scroller.getBoundingClientRect().top - 16,
          behavior: "smooth",
        })
      } else {
        window.scrollBy({ top: offset - 80, behavior: "smooth" })
      }
    }
    const attach = () => {
      const doc = frame.contentDocument
      if (!doc) return
      observedDocument?.removeEventListener("toggle", resize, true)
      observedDocument?.removeEventListener("click", handleFragmentClick, true)
      observedDocument = doc
      doc.addEventListener("toggle", resize, true)
      doc.addEventListener("click", handleFragmentClick, true)
      observer?.disconnect()
      // 链接分类处理：
      // - #锚点：邮件里的「中文/English」切换按钮就是这种，必须在 frame 内跳转。
      //   之前给所有链接都套 target=_blank，点一下会弹出一个 about:srcdoc#zh 的空标签页，
      //   看着就像聊天窗口闪了一下——用户报的「点中文按钮类似闪退」就是这个。
      // - http(s)/mailto/tel：新标签打开，并且不带 referrer，免得对方知道邮箱地址。
      // - javascript:/data:/cid: 之类：直接摘掉 href，点了不动。
      doc.querySelectorAll("a[href]").forEach((link) => {
        const href = (link.getAttribute("href") || "").trim()
        if (href.startsWith("#")) {
          link.removeAttribute("target")
          link.removeAttribute("rel")
          return
        }
        if (/^(?:https?:|mailto:|tel:)/i.test(href)) {
          link.setAttribute("target", "_blank")
          link.setAttribute("rel", "noopener noreferrer")
          return
        }
        link.removeAttribute("href")
      })
      resize()
      if ("ResizeObserver" in window) {
        observer = new ResizeObserver(resize)
        observer.observe(doc.documentElement)
        if (doc.body) observer.observe(doc.body)
      }
      doc
        .querySelectorAll("img")
        .forEach((img) => img.addEventListener("load", resize, { once: true }))
    }
    frame.addEventListener("load", attach)
    return () => {
      frame.removeEventListener("load", attach)
      observer?.disconnect()
      observedDocument?.removeEventListener("toggle", resize, true)
      observedDocument?.removeEventListener("click", handleFragmentClick, true)
      timers.forEach((timer) => window.clearTimeout(timer))
    }
  }, [resize, srcDoc])

  return (
    <div className="relative">
      {blocked.count > 0 && !showRemote && (
        <button
          type="button"
          onClick={() => setShowRemote(true)}
          className="mb-2 rounded-md border bg-background px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
        >
          {uiText("显示远程图片")}
        </button>
      )}
      <iframe
        ref={iframeRef}
        data-lanqin-i18n-ignore
        title={translateUiText("邮件正文", language)}
        className={className || "block w-full border-0 bg-white"}
        sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
        referrerPolicy="no-referrer"
        srcDoc={srcDoc}
        style={{ height }}
      />
    </div>
  )
}
