import type { ComponentProps } from 'react'
import { memo, useEffect, useState } from 'react'
import Markdown from 'react-markdown'
import type { Components, ExtraProps, Options } from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { CodeBlock } from '@/components/conversation/CodeBlock'
import { cn } from '@/lib/utils'

/**
 * 助手消息正文的 Markdown 渲染（issue #87）。
 *
 * ── 选型：react-markdown + remark-gfm + lowlight（显式语言子集） ────────
 *
 * 【为什么不自己解析 / 不用 innerHTML】raw HTML 那一派（marked、markdown-it
 * 直出字符串 + dangerouslySetInnerHTML）要先"渲染成 HTML 再插进 DOM"，
 * 那就必须再配一个 sanitizer，而且 sanitizer 漏一条就是 XSS。这条路直接排除。
 *
 * 【react-markdown 为什么没有 HTML 注入面】它把 Markdown 解析成 hast，
 * 再由 hast-util-to-jsx-runtime 生成 **React 元素树**，全程不碰
 * dangerouslySetInnerHTML——在 node_modules 里 grep 过：
 * react-markdown/lib/index.js 与 hast-util-to-jsx-runtime/lib/index.js 里
 * 一处都没有。
 *
 * 【模型输出的原始 HTML 会怎样】remark-rehype 默认开着 allowDangerousHtml，
 * 原始 HTML 会以 raw 节点活下来；react-markdown 的 post() 再把它换成
 * **文本节点**（lib/index.js 里 `parent.children[index] = {type: 'text', ...}`）。
 * 实测：喂进去 `<div onclick="alert(1)">x</div>`，产出的是转义后的
 * `&lt;div onclick="alert(1)"&gt;x&lt;/div&gt;`——看得见、执行不了。
 * 这就是这里不加 rehype-raw 的原因：那一个插件的效果正是把这条路重新打开，
 * 之后就得靠 rehype-sanitize 兜底，等于自己造一个新的风险面。
 *
 * 【链接】react-markdown 默认的 urlTransform 会把 `javascript:` 之类的协议
 * 过滤掉，所以 `[点我](javascript:alert(1))` 不会变成一个可执行的链接。
 *
 * 【为什么不设 skipHtml】设了的话原始 HTML 会被整段丢掉。对话里"内容凭空
 * 少了一段"比"看见一段尖括号"更难排查，所以保持默认（转成可见文本）。
 *
 * 【依赖的版本依据】都是读 node_modules 里的 .d.ts 确认的，不是照文档写的：
 *   react-markdown@10.1.0  peerDependencies 是 react >=18（React 19 在范围内），
 *                          Options 里已无 inline / passNode 这些旧字段；
 *   remark-gfm@4.0.1       unified 生态的标准插件，只加语法不加 HTML 面；
 *   lowlight@3.3.0         同步执行（不需要 async 插件，所以能用同步的
 *                          Markdown 组件而不是 MarkdownAsync），
 *                          highlight.js@11.11.2 提供语言文法。
 *                          这两个在这里都是**动态 import**，而且不再借用
 *                          rehype-highlight 那个现成插件——理由见下面
 *                          loadHighlight 的注释。
 */

type Plugins = NonNullable<Options['rehypePlugins']>

const REMARK_PLUGINS = [remarkGfm]

/**
 * 高亮插件还没到位时用的空列表。
 *
 * 【必须是模块级常量】每次渲染现写一个 `[]`，react-markdown 会认为插件列表
 * 变了，整棵子树重挂——流式下就是每一帧重挂一次。
 */
const NO_REHYPE_PLUGINS: Plugins = []

/**
 * lowlight 实例的类型，以及 `createLowlight` 要的那张文法表的取值类型。
 *
 * 【为什么不直接 import type { LanguageFn } from 'highlight.js'】能 import
 * 到（已经加成了直接依赖），但那个名字是 lowlight 签名里参数的**上游来源**，
 * 两边将来分开升级就会对不上。从 lowlight 自己的签名上取，等于让它告诉我
 * 们它到底要什么——和 CodeBlock.tsx 里"从 ExtraProps 取 hast 类型"是同一个
 * 理由，也省得为一个类型再拉一条直接依赖。
 */
type Lowlight = ReturnType<(typeof import('lowlight'))['createLowlight']>
type LanguageFn = NonNullable<Parameters<(typeof import('lowlight'))['createLowlight']>[0]>[string]

/**
 * 语法高亮保留的语言子集（issue #116）。
 *
 * 【为什么必须有这张表】lowlight 的默认集是 `common`，也就是 highlight.js 的
 * **36 种语言**（languages/ 目录 384 个文件 2.7 MB 源码，其中被 common 圈中的
 * 那些光源码就有 ~290 KB）。首轮已经把高亮整体改成动态 import、挪出首屏
 * 关键路径，但这 36 种语言仍然一个不落地打进那个懒加载 chunk——会话页是
 * 应用的主页面，每个进来的人都要下这一份。
 *
 * 【挑的依据】两个来源的交集：这个仓库自己的技术栈，和模型答代码题时
 * 实际会写出来的东西。
 *   - go / typescript / javascript / python / sql / json / yaml / markdown
 *     —— 项目本体就是 Go 后端 + React/TS 前端 + Postgres，YAML 供给
 *     compose 与 CI，Markdown 是本仓库的文档格式；这几种同时也在模型
 *     输出里出现得最频繁。
 *   - xml —— 前端话题里 HTML/SVG 很常见；另外 javascript 与 typescript
 *     的 JSX 分支把 `xml` 当 subLanguage（读过 grammar 源码），不注册
 *     的话 tsx 代码块里标签那一段会退化成纯文本。
 *   - bash / shell —— 本地优先的部署问答里"贴一段命令"是最高频的形态。
 *     两者是不同文法（bash 管 `sh`/`zsh`，shell 管带提示符的会话），
 *     加起来 7 KB。
 *   - diff / dockerfile / makefile / ini —— 这个仓库自带 Dockerfile 和
 *     Makefile，改配置、看 patch 是本地部署场景的常见问题；ini 顺带
 *     覆盖 toml 一类的配置样例。
 *   - plaintext —— ```text``` 这类围栏本来就不该着色，注册它免得每次
 *     走一遍"不认识"的异常路径。
 *
 * 【明确砍掉的大块头】css（19 KB）、scss/less（各 ~20 KB）、swift（22 KB）、
 * php/cpp/ruby/java/rust/… —— 项目自身不写这些，模型在"本地 RAG 平台"
 * 这个问题域里也极少写。css 是这里唯一稍有争议的（它在 js/ts 里也是
 * subLanguage），但它是除 js/ts 之外最大的一块，且这份前端的样式全走
 * Tailwind，取舍下来仍以砍掉为先。
 *
 * 【砍掉的怎么优雅降级】不注册的语言会让 lowlight 抛 Unknown language，
 * 插件把这个异常吞掉、保留原文本，产物是保留 `<code class="language-xxx">`
 * 但没有 hljs-* token——CodeBlock 照旧渲染成无高亮的等宽块，语言名和
 * 复制按钮都还在。
 *
 * 【为什么用 thunk 而不是直接写 import() 数组】语言名是 lowlight 注册时
 * 的键，必须显式带上；写成 `{ 名字: () => import(路径) }` 让"名字"和
 * "文法"在同一个地方，不用再维护一份平行的字符串数组。
 */
const HIGHLIGHT_GRAMMARS: Record<string, () => Promise<{ default: LanguageFn }>> = {
  bash: () => import('highlight.js/lib/languages/bash'),
  diff: () => import('highlight.js/lib/languages/diff'),
  dockerfile: () => import('highlight.js/lib/languages/dockerfile'),
  go: () => import('highlight.js/lib/languages/go'),
  ini: () => import('highlight.js/lib/languages/ini'),
  javascript: () => import('highlight.js/lib/languages/javascript'),
  json: () => import('highlight.js/lib/languages/json'),
  makefile: () => import('highlight.js/lib/languages/makefile'),
  markdown: () => import('highlight.js/lib/languages/markdown'),
  plaintext: () => import('highlight.js/lib/languages/plaintext'),
  python: () => import('highlight.js/lib/languages/python'),
  shell: () => import('highlight.js/lib/languages/shell'),
  sql: () => import('highlight.js/lib/languages/sql'),
  typescript: () => import('highlight.js/lib/languages/typescript'),
  xml: () => import('highlight.js/lib/languages/xml'),
  yaml: () => import('highlight.js/lib/languages/yaml'),
}

/** 把上表的 thunk 全取回来，拼成 createLowlight 要的那张文法表。 */
function loadGrammars(): Promise<Record<string, LanguageFn>> {
  const names = Object.keys(HIGHLIGHT_GRAMMARS)
  return Promise.all(names.map((name) => HIGHLIGHT_GRAMMARS[name]())).then((mods) =>
    Object.fromEntries(
      mods.map((mod, index): [string, LanguageFn] => [names[index], mod.default]),
    ),
  )
}

/**
 * 这个插件碰得到的 hast 节点。
 *
 * 【为什么不 import type { Root } from 'hast'】hast 是 react-markdown 的传递
 * 依赖，pnpm 严格 node_modules 下 web/ 里没有这个名字（CodeBlock.tsx 里踩过
 * 同一个坑，TS2307）。从 react-markdown 的 ExtraProps 里取它自己那个 Element
 * 类型再拼出根，既拿得到真类型，也不会和它升级后的实际形状漂移。
 */
type HastElement = NonNullable<ExtraProps['node']>
type HastChild = HastElement['children'][number]
type HastTree = { children: HastChild[] }

/** 把 hast 子树里的文字拼回来。只有 text 节点带内容，其余递归下去。 */
function nodeText(node: HastChild): string {
  if (node.type === 'text') return node.value
  if (node.type === 'element') return node.children.map(nodeText).join('')
  return ''
}

/**
 * 取 `<code>` 上的语言名。
 *
 * 判据照抄 rehype-highlight 的 `language()`：逐个看类名，`no-highlight` /
 * `nohighlight` 表示明确不要高亮（返回 false），否则取第一个 `lang-` 或
 * `language-` 前缀后面的部分。`lang-` 要先判——`language-` 的前五个字符是
 * `langu`，两者不会互相误伤。
 */
function languageOf(node: HastElement): string | false | undefined {
  const list = node.properties?.className
  if (!Array.isArray(list)) return undefined

  let name: string | undefined
  for (const raw of list) {
    const value = String(raw)
    if (value === 'no-highlight' || value === 'nohighlight') return false
    if (!name && value.slice(0, 5) === 'lang-') name = value.slice(5)
    if (!name && value.slice(0, 9) === 'language-') name = value.slice(9)
  }
  return name
}

/**
 * 自己实现的 rehype 高亮插件（替代 rehype-highlight，issue #116）。
 *
 * 【为什么不用现成插件】rehype-highlight 是 lowlight 外面的一层薄壳，真正
 * 干活的全在 lowlight 里；但它的 `languages` 选项救不了这个 chunk：源码里
 * **无条件** `import {common, createLowlight} from 'lowlight'`，而
 * `const languages = settings.languages || common` 让 `common` 永远是一个
 * 被引用的活绑定。实测（vite build，未压缩）——只给 rehypeHighlight 传
 * `languages` 子集：它那个 chunk 从 167,459 B 只降到 132,060 B（`common`
 * 里的 36 种语言一个没少，产物里 grep 得到 swift / Perl / Kotlin），
 * 而我这边 16 个语言各自成 chunk 另有 37,824 B——会话页那一串加起来
 * 338,916 B，比不改（334,530 B）还大 4.4 KB，请求数从 1 个变成 17 个。
 * 换成 lowlight 的 createLowlight 自己建实例，`common` / `all` 都引不到
 * （lowlight 的 package.json 是 `sideEffects: false`，named export 摇得干净），
 * lowlight 那个 chunk 落到 22,530 B、16 个语言合计 36,681 B，
 * 会话页那一串总量 228,119 B——比不改少 106,411 B（-31.8%）。
 *
 * 【行为上只差一处】rehype-highlight 对"不是 Unknown language"的异常会继续
 * 往外抛，这里一律吞掉：高亮是纯装饰，不该有任何一种输入把消息正文带走。
 * 换完之后实测过四种输入（临时起过一条测试，跑完删了）：
 *   ```go```     → `class="hljs language-go"` + 4 个 hljs-* span
 *   ```python``` → 同上，有 token
 *   ```css```    → `class="hljs language-css"`，0 个 span（子集外，降级）
 *   无语言围栏   → 不加 `hljs`（rehype-highlight 在 unshift 之前就 return）
 * 现有测试只钉了"不认识的语言没有 span"这一条，所以改这几十行时要自己
 * 补一次上面这组验证，别只看 MessageBubble 绿没绿。
 *
 * 【为什么不用 walk/visit 依赖】unist-util-visit 和 hast-util-to-text 都是
 * rehype-highlight 的传递依赖，同样 import 不到；这里要做的只是"找到
 * `pre > code`"和"把文字拼起来"，自己递归十几行比再拉两个直接依赖划算。
 */
function highlightPlugin(lowlight: Lowlight) {
  /** 给一个 `pre > code` 上色；没语言、语言不在子集里时原样留着。 */
  function highlightCode(node: HastElement) {
    const lang = languageOf(node)
    // 没写语言时不猜（rehype-highlight 的 detect 默认也是关的），标了
    // no-highlight 的同样跳过——两种情况都保持纯等宽块。
    if (!lang) return

    // 【hljs 这个类名要加在 try 之前】rehype-highlight 就是这么做的：语言
    // 认不认识都先打上 `hljs`。index.css 里 `.chat-markdown pre code.hljs`
    // 那条判据依赖它，去掉会让"不认识的语言"连底色都和别人不一样。
    const properties = (node.properties ??= {})
    if (!Array.isArray(properties.className)) properties.className = []
    if (!properties.className.includes('hljs')) properties.className.unshift('hljs')

    try {
      const result = lowlight.highlight(lang, nodeText(node), { prefix: 'hljs-' })
      // 【为什么要过滤掉 doctype】lowlight 只会产出 element/text 节点，但它的
      // 返回类型是 hast 的 Root，children 里按类型还允许 Doctype，直接赋值
      // 过不了 tsc。过滤比 `as` 诚实，而且真出了 doctype 也不会塞进 code 里。
      const tokens = result.children.filter(
        (child): child is HastChild => child.type !== 'doctype',
      )
      if (tokens.length > 0) node.children = tokens
    } catch {
      // 子集之外的语言：保留原文，上层照旧渲染成无高亮的等宽块。
    }
  }

  function walk(children: HastChild[]) {
    for (const node of children) {
      if (node.type !== 'element') continue
      if (node.tagName === 'pre') {
        for (const child of node.children) {
          if (child.type === 'element' && child.tagName === 'code') highlightCode(child)
        }
      }
      walk(node.children)
    }
  }

  // unified 的插件是"attacher"：调用一次拿到 transformer，之后每次渲染
  // 都只跑 transformer。lowlight 实例因此只建一次。
  return () => (tree: HastTree) => {
    walk(tree.children)
  }
}

/**
 * 语法高亮插件按需加载（issue #116）。
 *
 * 【为什么是动态 import】lowlight + highlight.js 那一栈原来静态 import，
 * 整个进会话页那一个 chunk。实测（vite build，未压缩）：拆之前
 * ConversationPage 334,130 B（gzip 104.2 KB），其余页面都在 20 KB 以下；
 * 拆之后 ConversationPage 167,071 B + 高亮自己的 167,459 B（gzip 51.3 +
 * 53.6 KB），而且 index.html 的 modulepreload 清单里没有它——首屏不等它。
 * 配上语言子集后高亮那部分变成 lowlight 22,530 B + 16 个语言 36,681 B。
 *
 * 【为什么要在 import() 后面**立刻** .then 取函数】把 `import('lowlight')`
 * 的结果先塞进 Promise.all、再从数组元素上取属性（`mod.createLowlight`）
 * 时，实测 rolldown 会把那个命名空间当成"所有导出都要"——lowlight 的
 * `export {grammars as all}`（192 种语言）被整块打进来，产物是一个
 * 869.70 kB 的 lowlight-*.js，比不用子集还大 5 倍。改成
 * `import('lowlight').then((mod) => mod.createLowlight)`、让属性访问跟着
 * import 走，摇树就认得。改这一行之前先看一眼产物里 lowlight-*.js 有没有
 * 超 100 kB。
 *
 * 【语言各自成 chunk 是代价】16 个文法各是一个动态入口，于是产物里是 16 个
 * 小文件、17 个请求（改之前是 1 个）。字节上划得来（部分网络下仍是并行取），
 * 但要是哪天想合成一个，得在 vite.config.ts 的 codeSplitting 里给
 * highlight.js/lib/languages 开一个组——那是另一处文件，没在这轮改。
 *
 * 【留下的一半是什么】react-markdown + remark-gfm 那一栈。它是**同步路径**上
 * 的东西（消息正文靠它渲染，不能等），所以只能留在页面 chunk 里。
 *
 * 【为什么模块级只取一次】每条历史消息都是一个 MarkdownContent，各自 import
 * 一次就是几十个 promise（模块缓存会去重，但会有几十次 setState → 一次重新
 * 解析）。合成一个 promise，所有实例等同一份。
 *
 * 【模块级就发起】不等到 useEffect：这个模块是会话页 chunk 的一部分，求值的
 * 那一刻就发出请求，和消息列表的请求并行——等消息渲染出来时插件多半已经
 * 到位，用户看不到"先无色后着色"这一跳。
 */
let highlightPromise: Promise<Plugins> | null = null
let highlightPlugins: Plugins | null = null

function loadHighlight(): Promise<Plugins> {
  highlightPromise ??= Promise.all([
    import('lowlight').then((mod) => mod.createLowlight),
    loadGrammars(),
  ])
    .then(([createInstance, languages]) => {
      highlightPlugins = [highlightPlugin(createInstance(languages))]
      return highlightPlugins
    })
    // 【拿不到就无声降级】高亮是纯装饰：取不到 chunk（离线、缓存坏了）时按
    // 无高亮渲染，**不能**让一次 cosmetic 的加载失败把消息正文一起带走。
    // CodeBlock 本来就处理"语言不认识"的情形，那条路是现成的。
    .catch(() => NO_REHYPE_PLUGINS)
  return highlightPromise
}

void loadHighlight()

type HeadingProps = ComponentProps<'h1'> & ExtraProps

/**
 * Markdown 的标题不落成 h1–h6。
 *
 * 【为什么】README §12 是硬判据：一个页面有且只有一个 h1，且层级不许跳
 * （对话页恰恰是"有意不设标题"的那个例外）。模型写出的 `# 结论` 是答案的
 * 排版，不是这一页的大纲——落成真标签就会往会话页里塞一个 h1，或者从
 * `###` 起步制造断层，靠标题在页面里跳转的人会直接跳进模型编的树里。
 * 视觉上的粗体与大字号照旧，丢的只是语义层级，而那个层级本来就不属于
 * 这份文档。
 */
function MarkdownHeading({ children, node: _node, className, ...rest }: HeadingProps) {
  return (
    <div {...rest} className={cn('mt-4 font-semibold first:mt-0', className)}>
      {children}
    </div>
  )
}

/**
 * 覆盖表放在模块作用域：每次渲染新建一个对象会让 react-markdown 认为
 * 组件类型变了，整棵子树重挂——流式下每一帧都重挂一次。
 */
const COMPONENTS: Components = {
  pre: CodeBlock,
  h1: MarkdownHeading,
  h2: MarkdownHeading,
  h3: MarkdownHeading,
  h4: MarkdownHeading,
  h5: MarkdownHeading,
  h6: MarkdownHeading,
}

type Props = {
  content: string
  className?: string
}

/**
 * 【为什么 memo】流式回答每来一个 token 就重渲染一次会话页。没有 memo 的话，
 * 屏幕上每一条历史消息都要重新解析一遍 Markdown 并重建元素树——实测一个
 * 5.6 KB 的答案单次解析+生成元素树约 10.5 ms（见交付说明），几十条历史
 * 消息叠起来就是每帧几百毫秒。memo 之后只有"正在增长的那一条"会重算。
 *
 * 【高亮插件到位时会重算一次】初始状态是没有插件的（chunk 还在路上），
 * 插件到了才切过去，那一刻已挂载的每条消息会重新解析一遍——这是一次性的，
 * 不是每帧一次。换成"等插件再渲染"（React.lazy + Suspense）能省掉这一次，
 * 代价是首屏会出现一帧"只有原文没有排版"的回落，而且 MessageBubble 那几条
 * 断言 DOM 结构的测试全要改成异步。这一跳更便宜，也更好验证。
 */
export const MarkdownContent = memo(function MarkdownContent({ content, className }: Props) {
  const [rehypePlugins, setRehypePlugins] = useState<Plugins>(
    () => highlightPlugins ?? NO_REHYPE_PLUGINS,
  )

  useEffect(() => {
    // 模块级已经发起过了（也可能是上一次挂载就取回来了），不必再等一次。
    if (highlightPlugins) return

    // 【alive 这道闸不能省】卸载后 setState 会报"更新一个已经卸载的组件"。
    let alive = true
    void loadHighlight().then((plugins) => {
      if (alive) setRehypePlugins(plugins)
    })
    return () => {
      alive = false
    }
  }, [])

  return (
    <div className={cn('chat-markdown', className)}>
      <Markdown
        remarkPlugins={REMARK_PLUGINS}
        rehypePlugins={rehypePlugins}
        components={COMPONENTS}
      >
        {content}
      </Markdown>
    </div>
  )
})
