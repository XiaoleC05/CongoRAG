import { describe, expect, it } from 'vitest'

/**
 * 页面的标题层级（issue #95）。
 *
 * 【为什么是"读源码"而不是"渲染每个页面"】渲染一个页面要 mock 掉它全部的
 * 数据 hook 和路由上下文（有的还要 QueryClient），八个页面就是八套脚手架；
 * 而这条约定本身是结构性的——"这个页面里有没有一个 h1"——不依赖运行时数据。
 * 用 Vite 的 ?raw 把文件当文本读进来，检查既准确又不用维护脚手架。
 *
 * 【它防的是什么】OnboardingPage 曾经只有两个 h3、没有 h1：文档大纲里出现
 * "有三级标题但没有一级"的断层，读屏软件按标题跳转的人跳不到这一页。
 * 这类缺陷不会报错、不会被类型检查发现，只能靠一条约定钉住。
 *
 * 【为什么标题层级要用真标签】视觉上的大小是排版决定，层级是语义决定，
 * 两者可以不一致（引导页的 h2 字号比 h1 小）。但"看起来像标题的 div"
 * 永远不会出现在读屏软件的大纲里——所以页面主标题必须是 h1 标签本身。
 */
const pages = import.meta.glob('./*.tsx', { query: '?raw', import: 'default', eager: true })

/** 页面文件（排除同目录下的 *.test.tsx）。 */
const PAGE_SOURCES = Object.entries(pages)
  .filter(([path]) => !path.includes('.test.'))
  .map(([path, source]) => ({ name: path.replace('./', ''), source: stripComments(source) }))

/**
 * 数标题之前先把注释去掉。
 *
 * 【为什么需要这一步】注释里出现一个标签的字面量（比如"真正的 h1 标签放在这里"）
 * 会被算成一次命中。这不是假设：这个文件的第一版就把引导页的注释数成了标题，
 * 于是它一边报"引导页没有 h1"、一边那个 h1 明明写着。
 *
 * 【为什么不做完整解析】只需要认这个项目实际会写的两种注释（JSX 里那种花括号包起来
 * 的块注释也落在"块注释"这一条上）：块注释、整行的行注释。行注释只认
 * "行首（允许缩进）的双斜杠"——URL 里的双斜杠在字符串里，按行扫下去会把同一行
 * 后面的代码一起吃掉，那样造成的漏报比注释误报更难查。
 */
function stripComments(source: string) {
  return (
    source
      // 块注释：JSX 里的那种（外面多一对花括号）也落在这条——去掉注释后会
      // 剩下一个空的花括号，对"数标题"没有影响。
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/^\s*\/\/.*$/gm, '')
  )
}

/**
 * 有意不设标题的页面——**加进来之前先问：是真的不需要，还是忘了写。**
 *
 * 对话页（ConversationPage.tsx）：主体是一列消息，页面没有"这一页叫什么"这个
 * 信息（契约里会话没有标题字段），而且它一进页面就把焦点交给输入框
 * （autoFocus），用户要做的事是打字。硬塞一个标题只会制造一个不承载信息的
 * 标题。结论记在 web/README.md 的规范 §12。
 */
const NO_TITLE_BY_DESIGN = ['ConversationPage.tsx']

describe('页面标题层级', () => {
  it('除有意例外外，每个页面都恰好有一个 h1', () => {
    const offending = PAGE_SOURCES.filter(({ name, source }) => {
      if (NO_TITLE_BY_DESIGN.includes(name)) return false
      // 匹配开始标签，要求后面跟空格或 >：<h1> 与 <h1 className=...> 都算，
      // 同时把 h1x 这种更长的名字排除掉。
      const count = source.match(/<h1[\s>]/g)?.length ?? 0
      return count !== 1
    }).map(({ name }) => name)

    // 写成"差集为空"而不是逐个断言：失败时打印的是缺 h1（或写了两个 h1）的文件名。
    expect(offending).toEqual([])
  })

  it('例外清单本身是准的：清单里的页面确实没有 h1，且文件存在', () => {
    const names = PAGE_SOURCES.map((p) => p.name)

    for (const exempt of NO_TITLE_BY_DESIGN) {
      // 文件改名后清单里会留下一个查不到的名字，等于这条检查静默失效——
      // 这正是"例外清单"最容易腐烂的方式，所以先钉住它存在。
      expect(names).toContain(exempt)

      const source = PAGE_SOURCES.find((p) => p.name === exempt)?.source ?? ''
      // 哪天给它补上了 h1，这条会红：提醒把它从清单里删掉，
      // 否则上一条测试会对这个页面永远放行。
      expect(source).not.toMatch(/<h1[\s>]/)
    }
  })
})
