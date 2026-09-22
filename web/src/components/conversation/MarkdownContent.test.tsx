// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { MarkdownContent } from '@/components/conversation/MarkdownContent'

// 没有配 globals，@testing-library 的自动清理不会生效（它靠全局的 afterEach）。
afterEach(cleanup)

/** 高亮插件是动态 import 的，等它到位要跨好几轮事件循环，给足余量。 */
const PLUGIN_WAIT = { timeout: 10_000, interval: 50 }

function codeBlocks(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll('pre code'))
}

/**
 * 数一个代码块里真正的 token span。
 *
 * 【为什么不能数 span 就算数】降级的代码块里一个 span 都没有（原文是 text
 * 节点），而着色过的那些 token 全部带 `hljs-` 前缀——用前缀选择器数，
 * "有没有着色"才是一个判据，而不是"DOM 里有没有标签"。
 */
function tokenCount(block: HTMLElement | undefined): number {
  return block?.querySelectorAll('span[class^="hljs-"]').length ?? 0
}

/**
 * 等插件到位。
 *
 * 【为什么每条降级断言都要先过这一关】插件没到位时，渲染出来的是"纯等宽块"
 * 本身——"子集外的语言没有 token"在那个瞬间必然成立，断言是真的但什么也没
 * 证明（MessageBubble.test.tsx 里那条就是这样）。这里拿一个**子集内**的语言
 * 当探针：它着了色，才说明后面看到的"没有 token"来自异常分支，而不是来自
 * "插件还没来"。
 */
async function waitForPlugin(probe: HTMLElement | undefined) {
  await waitFor(() => {
    expect(tokenCount(probe)).toBeGreaterThan(0)
  }, PLUGIN_WAIT)
}

describe('MarkdownContent 的语法高亮（issue #116 换自实现插件后的回归）', () => {
  it('子集内的语言被着色，token 带 hljs- 前缀', async () => {
    const { container } = render(
      <MarkdownContent content={'```go\npackage main\n\nfunc main() {}\n```\n'} />,
    )

    await waitForPlugin(codeBlocks(container)[0])

    const code = codeBlocks(container)[0]
    // hljs 这个类名是插件加在 try 之前的：index.css 的代码块底色判据靠它。
    expect(code.className).toContain('hljs')
    expect(code.className).toContain('language-go')
    expect(code.textContent).toContain('func main()')
  })

  // 【这条是 issue #116 的核心】插件从 rehype-highlight 换成自己写的之后，
  // "语言不在 HIGHLIGHT_GRAMMARS 里"这条路就只剩我们自己兜——lowlight 对
  // 未注册的语言是**抛异常**（读过 lowlight/lib/index.js：`Unknown language`
  // 那行 throw），一旦没接住，react-markdown 的整棵渲染树都会倒，用户看到的
  // 是白屏而不是"一段没颜色的代码"。
  it('子集外的语言降级成无高亮的等宽块，正文照旧可见', async () => {
    const { container } = render(
      <MarkdownContent
        content={'结论如下：\n\n```go\npackage main\n```\n\n```css\n.a { color: red }\n```\n'}
      />,
    )

    const blocks = codeBlocks(container)
    // 两个围栏都渲染成了代码块（css 那块没有被丢掉）
    expect(blocks).toHaveLength(2)
    await waitForPlugin(blocks[0])

    const css = blocks[1]
    expect(tokenCount(css)).toBe(0)
    // 原文一字不少——"没高亮"不等于"内容没了"
    expect(css.textContent).toContain('.a { color: red }')
    // 降级的块仍然带 hljs 类名（unshift 在 try 之前），底色和着色过的块一致
    expect(css.className).toContain('hljs')
    expect(css.className).toContain('language-css')
    // 降级不影响外壳：语言名还在，复制按钮还在、还叫得出对象名
    expect(screen.getByRole('button', { name: '复制 css 代码' })).toBeTruthy()
    // 正文没被一次装饰性的失败带走
    expect(container.textContent).toContain('结论如下：')
  })

  it('同一棵树里，能着色的照样着色、不能着色的照样降级，互不影响', async () => {
    const { container } = render(
      <MarkdownContent
        content={'```python\nprint(1)\n```\n\n```rust\nfn main() {}\n```\n\n```sql\nselect 1;\n```\n'}
      />,
    )

    const blocks = codeBlocks(container)
    expect(blocks).toHaveLength(3)
    await waitForPlugin(blocks[0])

    // python / sql 在子集里
    expect(tokenCount(blocks[0])).toBeGreaterThan(0)
    expect(tokenCount(blocks[2])).toBeGreaterThan(0)
    // rust 明确被砍掉了（见 HIGHLIGHT_GRAMMARS 的注释）
    expect(tokenCount(blocks[1])).toBe(0)
    expect(blocks[1].textContent).toContain('fn main() {}')
  })

  it('没写语言的围栏保持纯等宽块，不被猜成某种语言', async () => {
    const { container } = render(
      <MarkdownContent content={'```go\npackage main\n```\n\n```\n裸围栏\n```\n'} />,
    )

    const blocks = codeBlocks(container)
    expect(blocks).toHaveLength(2)
    await waitForPlugin(blocks[0])

    const bare = blocks[1]
    expect(tokenCount(bare)).toBe(0)
    expect(bare.textContent).toContain('裸围栏')
    // 没写语言时连 hljs 类名都不加（插件在语言未知但**写了**语言时才走到
    // 打类名那一步），这条钉住"不猜"这个选择。
    expect(bare.className).not.toContain('hljs')
  })

  // 语言名来自模型输出，是不可信输入。它会原样变成 `language-xxx` 这个类名
  // 再喂给 lowlight——异常路径没人走过，所以拿一个明显畸形的名字钉一条：
  // 不抛、不落成标签、原文还在。
  it('畸形语言名也走降级，不会把标签注进 DOM', async () => {
    const { container } = render(
      <MarkdownContent content={'```go\npackage main\n```\n\n```"><script>x</script>\ncode\n```\n'} />,
    )

    const blocks = codeBlocks(container)
    expect(blocks).toHaveLength(2)
    await waitForPlugin(blocks[0])

    expect(tokenCount(blocks[1])).toBe(0)
    expect(container.querySelector('script')).toBeNull()
    expect(blocks[1].textContent).toContain('code')
  })
})
