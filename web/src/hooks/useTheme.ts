import { useCallback, useState } from 'react'

/** 主题选择存在 localStorage 的键。index.html 的防闪白脚本读的是同一个键。 */
const STORAGE_KEY = 'congorag-theme'

export type Theme = 'dark' | 'light'

/** 真实状态以 <html> 上的类名为准，不用 React 状态当真相——那个同步脚本会先改它。 */
function readTheme(): Theme {
  return document.documentElement.classList.contains('dark') ? 'dark' : 'light'
}

/**
 * 深色/浅色切换。
 *
 * 深色是默认值（写死在 index.html 的 <html class="dark"> 上），
 * 这个 hook 只负责用户显式切换后改写类名和 localStorage。
 */
export function useTheme() {
  const [theme, setTheme] = useState<Theme>(readTheme)

  const toggle = useCallback(() => {
    const next: Theme = readTheme() === 'dark' ? 'light' : 'dark'
    document.documentElement.classList.toggle('dark', next === 'dark')
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // localStorage 被禁用时不影响切换，只是刷新后回到默认
    }
    setTheme(next)
  }, [])

  return { theme, toggle }
}
