// 首帧之前把深色模式挂上，避免刷新时先白一下再变黑。
//
// 必须放在 <head> 里用普通 <script src> 同步加载：生产 CSP 是
// script-src 'self' ...，没有 'unsafe-inline'，内联脚本会被直接拦掉。
try {
  if (localStorage.getItem("lanqin:theme") === "dark") {
    document.documentElement.classList.add("dark")
    document.documentElement.style.colorScheme = "dark"
  }
} catch (error) {
  /* localStorage 被禁用时忽略，跟随后续 React 初始化 */
}
