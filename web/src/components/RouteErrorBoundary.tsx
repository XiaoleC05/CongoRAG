import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

type Props = { children: ReactNode }
type State = { error: Error | null }

/**
 * 路由子树的错误边界。
 *
 * 【为什么它不是可选项，而是懒加载的配套】
 * React.lazy 的动态 import 会 reject。真实场景：换版本时二进制被替换，
 * 而浏览器里还开着上一版的 index.html——那页 HTML 指向的 chunk 文件名带 hash，
 * 构建时的 emptyOutDir 已经把旧文件删了，于是 import() 404，
 * 整个 lazy 组件 reject。
 *
 * 没有边界时 React 会把【整棵树】卸载 → 白屏。控制台只有一行
 * "Failed to fetch dynamically imported module"，比一次普通崩溃更难查：
 * 页面全白，没有 stack 指向任何一处业务代码。有了边界，
 * 至少侧栏和顶栏还在（它挂在 AppLayout 的内容容器里），
 * 用户看到的是一句能读懂的说明 + 一个能自救的按钮。
 *
 * 【为什么必须给"刷新页面"按钮】React 会把 lazy 的失败结果缓存住，
 * 再渲染同一个组件也不会重新发起 import——组件树自己恢复不了，
 * 唯一的出路是整页重载（重新拿到 index.html 和新的一批 chunk）。
 * 给一个"重试"按钮是骗人的：它什么都不会发生。
 *
 * 【为什么不用 ErrorText】ErrorText 是给后端 RFC 7807 错误用的，
 * 按 type 字段选中文文案。这里拿到的是浏览器/打包器抛的普通 Error，
 * 没有 type 也没有 detail，硬套只会落到兜底分支显示一句无关的默认文案。
 *
 * 【为什么是 class】React 19 仍然只有类能当错误边界
 * （getDerivedStateFromError / componentDidCatch 没有函数式等价物）。
 *
 * 【调用方要用 key 让它复位】见 layouts/AppLayout.tsx：key={pathname}，
 * 换了路由就换一个实例，否则一次失败会把后面所有页面都盖住。
 */
export class RouteErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 边界把错误吞掉了，这里再不打控制台就彻底没有线索了。
    // 生产环境没有 sourcemap 时 stack 是压缩过的，但 componentStack 仍然
    // 能指出是哪条路由挂的。
    console.error('页面渲染失败', error, info.componentStack)
  }

  render() {
    const { error } = this.state

    if (!error) {
      return this.props.children
    }

    return (
      <div className="mx-auto max-w-3xl p-6">
        <Alert variant="destructive">
          <AlertTitle>页面加载失败</AlertTitle>
          <AlertDescription>
            <p>
              这一页没能加载出来。刷新后如果还是失败，说明当前版本的静态资源不完整。
            </p>
            {/* 原文照贴。chunk 404 和真正的渲染崩溃在控制台里长得很像，
                把浏览器给的那句话摆到用户能截图的地方，排查少一轮来回。 */}
            <p className="mt-2 font-mono text-xs break-all">{error.message}</p>
          </AlertDescription>
        </Alert>

        <Button className="mt-4" onClick={() => window.location.reload()}>
          刷新页面
        </Button>
      </div>
    )
  }
}
