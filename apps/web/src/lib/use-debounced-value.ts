import * as React from "react"

/**
 * 把高频变化的值（搜索框输入等）压到 delay 毫秒后再生效。
 * 输入框本身仍然即时回显，只是下游请求/过滤用这个值。
 */
export function useDebouncedValue<T>(value: T, delay = 300): T {
  const [debounced, setDebounced] = React.useState(value)
  React.useEffect(() => {
    if (value === debounced) return
    const timer = window.setTimeout(() => setDebounced(value), delay)
    return () => window.clearTimeout(timer)
  }, [value, delay, debounced])
  return debounced
}
