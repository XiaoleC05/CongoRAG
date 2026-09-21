import { useCallback, useSyncExternalStore } from 'react'

/** 主题选择存在 localStorage 的键。index.html 的防闪白脚本读的是同一个键。 */
const STORAGE_KEY = 'congorag-theme'

export type Theme = 'dark' | 'light'

/** 真实状态以 <html> 上的类名为准，不用 React 状态当真相——那个同步脚本会先改它。 */
function readTheme(): Theme {
  return document.documentElement.classList.contains('dark') ? 'dark' : 'light'
}

/**
 * "类名变了"的订阅者集合。
 *
 * 模块级的 Set，不是组件状态：主题是全局的一个值，谁订阅谁就在切换时重新读一次 DOM。
 */
const listeners = new Set<() => void>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * 快照。返回的是 'dark' | 'light' 这样的原始字符串——按值比较，
 * 主题没变时引用天然稳定，useSyncExternalStore 不会因此空转重渲染。
 * 【不要改成返回对象】那会导致每次调用都是新引用，触发无限重渲染。
 */
function getSnapshot(): Theme {
  return readTheme()
}

/**
 * 深色/浅色切换。
 *
 * 深色是默认值（写死在 index.html 的 <html class="dark"> 上），
 * 这个 hook 只负责用户显式切换后改写类名和 localStorage。
 *
 * 【为什么用 useSyncExternalStore 而不是 useState】真相只有一个——<html> 上的
 * 类名（防闪白脚本、CSS 的 .dark 变体都认它）。useState 会在每个实例里各存
 * 一份快照，第二个消费者永远不会再去读 DOM：在一个地方切换主题后，另一处会
 * 永久停在旧值上，而且不报错。useSyncExternalStore 不引入第二个真相源，
 * 它只是把"类名变了"广播给所有订阅者，由订阅者自己重新读 DOM。
 */
export function useTheme() {
  const theme = useSyncExternalStore(subscribe, getSnapshot)

  const toggle = useCallback(() => {
    const next: Theme = readTheme() === 'dark' ? 'light' : 'dark'
    document.documentElement.classList.toggle('dark', next === 'dark')
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // localStorage 被禁用时不影响切换，只是刷新后回到默认
    }
    // 先落盘再广播：订阅者被叫醒时读到的已经是新值，
    // 反过来（先通知后写 DOM）会让它们读到切换前的旧类名。
    for (const listener of listeners) listener()
  }, [])

  return { theme, toggle }
}
