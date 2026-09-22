# ConGoRAG 前端

React SPA。产物被 `go:embed` 打进 api 二进制，由 Go 进程在 `:3210` 直接提供——**没有 Node 服务器，没有 SSR**。

**新增页面前先读完这份文档。** 里面的约定是为了让后面每个模块长得一样，不是建议。

---

## 技术栈

| 用途 | 选了 | 备注 |
| --- | --- | --- |
| 构建 | Vite 8（底层是 rolldown） | |
| 语言 | TypeScript 6 | `verbatimModuleSyntax: true`，纯类型导入必须写 `import type` |
| UI 库 | React 19 | |
| 路由 | react-router 8（**声明式模式**） | `react-router-dom` 在 v8 已被删除 |
| 数据 | TanStack Query 5 | 已经承担数据层，不用路由的 loader/action |
| 样式 | Tailwind CSS 4（CSS-first） | **没有 `tailwind.config.js`**，主题在 `src/index.css` |
| 组件 | shadcn/ui（base = **radix**，preset = nova） | 源码在 `src/components/ui/`，**直接改会丢** |
| 图标 | lucide-react | |
| 静态检查 | oxlint | `make lint` |

---

## 目录结构

```text
web/
├── index.html              深色优先的 <html class="dark"> + 防闪白脚本
├── package.json
├── vite.config.ts          插件 / 路径别名 / 构建输出 / 开发代理
├── components.json         shadcn 配置
├── .oxlintrc.json          lint 规则 + ignorePatterns
├── tsconfig.json           根：只有 references
├── tsconfig.app.json       src/ 的配置，@/ 路径别名写在这里
├── tsconfig.node.json      vite.config.ts 的配置
├── public/                 原样拷进产物的静态文件（只放不经构建的东西，如 favicon）
└── src/
    ├── main.tsx            入口：Provider 链
    ├── router.tsx          路由表
    ├── index.css           Tailwind 入口 + 主题令牌（shadcn 生成，可改）
    ├── layouts/            布局路由的组件（AppLayout）+ 侧栏导航表（nav.ts）
    ├── pages/              一个路由一个文件，默认导出（每个页面要有 h1，见 §12）
    ├── components/
    │   ├── ui/             shadcn 生成，不要手改
    │   ├── <域>/           业务组件，如 knowledge/
    │   └── ErrorText.tsx   跨域的通用组件
    ├── hooks/              数据获取 + 通用逻辑
    └── lib/                纯函数工具（errors / format / validation / utils）
```

**放哪的判据**：

| 东西 | 放哪 |
| --- | --- |
| 带请求的逻辑 | `hooks/` |
| 无副作用的纯函数 | `lib/` |
| 一个路由的入口，默认导出 | `pages/` |
| 布局路由用的壳 | `layouts/` |
| 布局专用的常量（导航表之类） | `layouts/` 下的独立 `.ts`——和组件放同一个文件会打断 Fast Refresh |
| 业务组件（某个域专用） | `components/<域>/` |
| 跨域通用组件 | `components/` 根下 |
| 图片等不经构建的静态文件 | `public/` |

不要为了"将来可能复用"提前抽组件。**等第二个页面真的要用了再抽。**

---

## 规范

### 1. 组件不直接调 `api.*`，一律走 hooks

```tsx
// ✗ 页面里这样写，请求细节会散到每个页面
const { data } = useQuery({
  queryKey: ['knowledge-bases'],
  queryFn: () => api.GET('/api/v1/knowledge-bases'),
})

// ✓ 请求写在 hooks/ 里，页面只消费
const { data, isPending, error } = useKnowledgeBases()
```

**为什么**：缓存 key、错误转换、路径拼写只在一个地方。key 字面量散在多处时，拼错一处不会报错，只会让界面"点了没反应"。

**约定的 hook 形状**：

- 读：`useXxx()` → 返回 TanStack Query 的结果
- 写：`useXxxMutations()` → 返回 `{ create, rename, remove }` 之类的 mutation 对象

**文件命名**：`useXxx.ts` 用 camelCase，和导出的 hook 同名。项目里已有的 `use-mobile.ts` 是 shadcn 生成的，不动它，但**新写的都按 camelCase**。

### 2. 页面必须处理三种状态

```tsx
if (isPending) return <SkeletonGrid />
if (error)     return <Alert variant="destructive"><ErrorText error={error} /></Alert>
return <Grid data={data} />
```

漏掉加载态 → 首次进来闪一下空列表；漏掉错误态 → 请求失败时一片空白。**两种都不报错，只是体验坏掉**，所以容易漏。

空态是第四种，别和加载态合并。

### 3. 写操作必须作废缓存

```tsx
onSuccess: () => queryClient.invalidateQueries({ queryKey: KNOWLEDGE_BASES_KEY })
```

不作废的话，数据改成功了但页面不刷新——**用户点了按钮没反应，而且不报错**。

**缓存 key 的写法**：

```ts
export const KNOWLEDGE_BASES_KEY = ['knowledge-bases']          // 列表
export const knowledgeBaseKey = (id: string) => ['knowledge-bases', id]  // 单条
```

用 `invalidateQueries({ queryKey: KNOWLEDGE_BASES_KEY })` 会**按前缀**失效——所有以它开头的 key（含单条）一起作废。这是想要的：删掉一条之后详情缓存也该失效。

### 4. 错误展示统一走 `ErrorText`，按 `type` 分支

```tsx
import { ErrorText } from '@/components/ErrorText'
<ErrorText error={error} />
```

后端错误是 RFC 7807（`{ type, title, status, detail }`）。

**`type` 是机器读的枚举**，前端按它选文案（映射表在 `lib/errors.ts` 的 `MESSAGES`）；**`detail` 是后端可以随时改的文案，不要按它分支**，只作为排查信息展示。

已知的 `type` 取值：

| type | HTTP | 含义 |
| --- | --- | --- |
| `invalid_argument` | 400 | 提交的参数不合法（含路径参数格式错、请求体解析失败；也含"模型不支持工具调用"这类配置性拒绝） |
| `not_found` | 404 | 资源不存在，或引用的父资源不存在 |
| `conflict_duplicate_key` | 409 | 唯一约束冲突 |
| `conflict` | 409 | 状态冲突：非法的状态迁移、并发修改，以及"重新索引一份已经在排队的文档" |
| `embedding_change_requires_reindex` | 409 | **换 embedding 模型需要先确认清空重建**——引导页与设置页按它弹确认框，用户确认后带 `allowEmbeddingReset` 重发 |
| `state_schema_version_mismatch` | 409 | **这条 run 的快照是旧版本的代码写的**——恢复被明确拒绝，提示重新发起（issue #65） |
| `tool_effect_already_applied` | 409 | **这一步的副作用可能已经生效**，平台不能替你决定重不重放（issue #63 / ADR-007） |
| `replay_unsafe` | 409 | **这一步的工具不允许被自动重放**（`WRITE_NON_IDEMPOTENT` 或 `retry_policy = never`） |
| `upstream_llm_error` | 502 | 上游模型服务出错 |
| `context_overflow` | 400 | 上下文超出模型窗口（M3 起） |
| `internal_error` | 500 | 服务内部错误，详情在服务端日志 |

**后三个是 v4.0 新加的，而且它们只在恢复路径上出现**（`POST /api/v1/runs/{runId}/resume`）。
恢复端点把校验放在**开流之前**，所以它们是正常的 409 Problem；写成流里一帧
error 的话，客户端只能拿到一个笼统的 type（这一条在设计上是有意的，
见 `internal/agent/usecase.go` 里 `ResumePlan` 的注释）。

**它们也会出现在 SSE 的 error 帧上**（比如恢复跑到一半才失败），
所以 `platform.SSEErrorType` 里同样有这三档——`lib/errors.ts` 的 `MESSAGES`
要能认它们，否则会落到兜底分支显示后端原文。

**这张表和 `apps/api/internal/api/problem.go` 的 `classify()` 一一对应。** 那边加一条 sentinel，这边要跟着补；漏了不会报错，只会落到兜底分支显示后端原文。

不要在各处自己 `String(error)` 或 `JSON.stringify(error)`。

### 5. 类型从契约来，不手写

```tsx
import type { Schemas } from '@congorag/api-client'
type KnowledgeBase = Schemas['KnowledgeBase']
```

**永远不要手写和后端数据结构对应的 interface。** 它必须和 `contracts/openapi.yaml` 一致；手写的会悄悄过期。

接口不够用时，改 `contracts/openapi.yaml` 再 `make generate`——不要在前端贴一个"临时的"类型。

### 6. 表单校验：客户端省往返，后端才是强制点

```tsx
import { validateName } from '@/lib/validation'

const clientError = validateName(name)
// ...
<Button type="submit" disabled={pending || !!clientError}>
```

**客户端校验只是省一次白跑的往返。真正的强制在后端。** 前端漏了不会出安全问题，后端漏了才会。

契约里的 `minLength` / `maxLength` **不会**被生成器变成 Go 的运行时校验——`oapi-codegen` 生成的只是结构体字段。所以上限必须在 usecase 里显式写（见 `internal/knowledge/usecase.go` 的 `cleanName`）。

**长度上限的数字有三处必须一致**，而且没有代码生成能帮你同步：

1. `internal/knowledge/usecase.go` 的 `maxNameLen`（真正的强制点）
2. `contracts/openapi.yaml` 的 `maxLength`（给客户端看的声明）
3. `lib/validation.ts` 的 `MAX_NAME_LEN`（提交前的客户端校验）

**按字符数算，不按字节数**——中文一个字三字节，按字节算限制会随语言变化。

### 7. 写操作要清掉上一次的错误

```tsx
const openCreate = () => {
  create.reset()   // 不清的话，重开弹窗会看到上一次的报错
  setCreating(true)
}
```

### 8. 表单弹窗靠换 `key` 重置，不要用 effect 监听 `open`

```tsx
// 页面侧：每次打开递增，换 key 让组件重新挂载
const [formSeq, setFormSeq] = useState(0)
<NameDialog key={`create-${formSeq}`} open={creating} ... />
```

在组件里用 `useEffect` 监听 `open` 再 `setState` 会多渲染一次，oxlint 的 `react(set-state-in-effect)` 也会报。换 key 是 React 推荐的写法。

### 9. 样式：Tailwind 类名，不写内联 style，不新写 CSS 文件

```tsx
// ✗
<div style={{ padding: 24, maxWidth: 720 }}>

// ✓
<div className="max-w-3xl p-6">
```

**颜色只用语义 token**（`bg-background` / `text-muted-foreground` / `border-border` / `bg-card` / `text-destructive`…），不要写 `text-gray-500`、`bg-[#1a1a1a]` 这类硬编码色值——那样浅色模式下就废了。

需要新颜色时，在 `src/index.css` 的 `:root` / `.dark` 里加变量，再用 `@theme inline` 桥接成类名。

### 10. 深色优先

- `<html>` 默认带 `class="dark"`（写死在 `index.html`）
- 浅色只在用户切换后生效，存在 `localStorage` 的 `congorag-theme`
- **不要在组件里读系统偏好**——`dark:` 变体已经被 `@custom-variant` 改成类驱动，不跟系统走
- 防闪白靠 `index.html` 里那段**同步内联脚本**，不要挪进 React

### 11. 日期显示用 `lib/format.ts`

```tsx
import { formatDateTime } from '@/lib/format'
formatDateTime(kb.updatedAt)
```

**不要对后端的时间串做字符串切片。** 后端返回的是 api 进程的本地时间（交付期容器是 UTC），切片会把 UTC 当本地时间显示，差 8 小时且不报错。`formatDateTime` 用 `Date` 解析后按**浏览器时区**格式化。

---

### 12. 一个页面一个 `h1`，标题层级不许跳

- 页面有且只有一个 `h1`，它就是页面的主标题；下面的分组标题依次 `h2` / `h3`，不跳级。
- **不许用样式类伪造层级**。`<div className="text-xl font-semibold">` 不会出现在读屏软件的大纲里——很多用户靠快捷键在标题之间跳转，跳级和缺级都会让大纲断层。层级是标签的事，字号是排版的事，两者不必一致：引导页的 `h2` 字号比 `h1` 小，这是允许的。
- 例外只有一个：**对话页（`ConversationPage.tsx`）有意不设标题**。它主体是一列消息，页面没有"这一页叫什么"这个信息（契约里会话没有标题字段），而且进来就该在输入框里打字。**加例外之前先问：是真的不需要，还是忘了写。**
- 404 页的 `404` 那一行保持 `<p>`：它是状态码不是标题，做成标题只会让大纲里冒出两个同级标题。
- 判据：`src/pages/headingStructure.test.ts` 读 `src/pages/*.tsx` 检查这件事，例外清单在那个文件里；清单和现实对不上（页面补了 `h1` 却没从清单里删掉）也会红。

### 13. 状态变了要有人播报

三层分工，选错的表现是"读屏用户什么都没听到"或者"同一句话说两遍"：

| 场景 | 用什么 | 先例 |
| --- | --- | --- |
| 出错，必须立刻打断 | `role="alert"`（隐含 assertive），**不要再写 `aria-live`** | `ErrorToast.tsx` |
| 加载 / 进行中，可以等当前这句念完 | `role="status"`（或 `aria-live="polite"`）+ `aria-busy` | `PageFallback.tsx` |
| 用户自己点的、界面上本来就看得见的变化（列表刷新、按钮文案变成"上传中…"） | 不用 live region | — |

- 只有骨架、没有可见文字的加载态，把文字放 `sr-only`：界面上不需要"加载中…"这种废话，读屏用户需要。
- 判据：凡是"界面自己变了"的东西（加载完了、失败了、流式在追加），要么有一句会被念出来的话，要么变化就写在用户刚点的那个按钮上。

### 14. 键盘与焦点

- 交互元素必须是真控件（`button` / `a` / Radix 组件）。可点击的 `div` 要配齐 `role`、`tabIndex={0}` 和 `onKeyDown`（先例：`KnowledgeCard.tsx`）。
- 只有图标的按钮必须有可访问名，而且带上对象名——`aria-label="删除 报告.md"`，而不是 `aria-label="删除"`（读屏软件念一串"删除"等于没说）。
- **路由切换后把焦点搬到新页面**：`AppLayout.tsx` 的 `RouteFocus` 在页面内容挂载后聚焦页面的 `h1`（页面没有 `h1` 时聚焦那个内容容器——它是 `div[tabindex="-1"]`）。它挂在 `<Suspense>` **里面**，chunk 没到就不会挂载——所以焦点不会先落在骨架上、再也没机会播报标题。
- 焦点落点用 `tabIndex={-1}`（只能被脚本聚焦，不进 Tab 序列），聚焦框由 `index.css` 里那条 `[tabindex='-1']:focus` 去掉。**不要给可操作元素写 `outline-none`**：那会违反"焦点必须可见"。
- **不抢用户已经拿到的焦点**：页面自己 `autoFocus` 的元素（对话页、Agent 详情页的输入框）优先，`RouteFocus` 会跳过。
- 页面抛错时走 `RouteErrorBoundary`，那一帧 `RouteFocus` 已经不在了（连页面一起被换掉），播报靠报错 `Alert` 自己的 `role="alert"`——这条路径不需要焦点管理，别去给它补。
- **加载态也要有 `h1`**：焦点是在页面挂载那一刻找标题的，标题还没渲染出来就只能落在内容容器上（Agent 详情页就是这样——它的标题是 agent 的名字，名字还没拉回来）。这不是缺陷，但新页面尽量先把标题渲染出来（静态文案，或者像知识库详情页那样用占位），播报才稳定。
- **整页只有一个 `main` 地标**：shadcn 的 `SidebarInset` 自己就是 `<main>`，所以 `AppLayout` 里的滚动容器是 `div`——再套一个就有两个 main，读屏软件按地标跳转的人会撞见两个"主要内容区"（这个重复是 issue #94 顺手修掉的，`AppLayout.test.tsx` 有断言钉住）。
- 判据：`AppLayout.test.tsx` 的焦点用例有三条（进入页面、切换路由、不抢输入框焦点）。手工验收：Tab 到侧栏 → 回车 → 下一次 Tab 应该从新页面的内容开始。

### 15. 响应式：每一页都要显式决定窄屏行为

- 逐页决定"什么消失、什么折叠、什么变成抽屉、什么从表格变卡片"，并且写下来。**反对把桌面布局直接缩小**——那既不是设计，窄屏上也不可用。
- 不要用文本长度决定布局，不要魔法像素值。
- **悬停交互必须有手指的等价物**：鼠标能悬停看到的东西，触屏上要能点开或长按。

#### 三个档位（issue #86 定下来的）

| 档 | 宽度 | 依据 |
| --- | --- | --- |
| 手机竖屏 | `<640px`（Tailwind 无前缀） | 390px（iPhone 14 逻辑宽度）是实测基准 |
| 平板 | `640–1023px`（`sm:` 生效、`lg:` 之前） | 这一档**仍然用卡片的形态**，见下面"为什么表格等到 `lg:`" |
| 桌面 | `>=1024px`（`lg:` 起） | 侧栏展开占 256px，正文这时才有 720px |

侧栏另外有一条自己的分界：`hooks/use-mobile.ts` 的 `MOBILE_BREAKPOINT = 768`，`<768px` 时 shadcn 的 `Sidebar` 变成 Sheet 抽屉（点 `button[data-sidebar="trigger"]` 打开）。它落在平板档的中间——**这不是遗漏**：导航的形态和正文的形态是两件事，导航在 768px 就该收起来了。**已实测**（390px 下点触发器，`[role="dialog"]` 从 0 变 1），不用再查。

#### 逐页结论

| 页面 | `<640` 手机 | `640–1023` 平板 | `>=1024` 桌面 |
| --- | --- | --- | --- |
| 知识库列表 `KnowledgeBasesPage` | 卡片网格 1 列（`auto-fill` + `minmax(min(280px,100%),1fr)`） | 640 是 2 列，**768 起反而回到 1 列**（侧栏这时展开，占掉 256px） | 1024 是 2 列，1280 是 3 列 |
| 知识库详情 `KnowledgeBaseDetailPage` | 标题与三个按钮**分两行**；文档列表是**卡片**，每个数字自带名字（分块 1 · 1.1 KB · 时间） | 同上（`lg:` 之前一直是卡片） | 一行两端对齐 + 六列表格 |
| 对话列表 `ConversationsPage` | 行式列表：标题截断、时间与 `···` 不参与挤压 | 同左 | 同左 |
| 会话页 `ConversationPage` | 气泡上限放宽到 85%（窄屏上 75% 只剩约 250px，中文每行十来个字） | 同左 | 气泡上限 75% |
| Agent 列表 `AgentsPage` | 卡片网格 1 列 | 同上（640 两列、768 起一列） | 1024 两列、1280 三列 |
| Agent 详情 `AgentDetailPage` | 标题 + 编辑按钮一行；运行历史是行式列表，输入截断、时间与状态徽章不挤压 | 同左 | 同左 |
| 执行轨迹 `RunTracePage` | 一步一张卡（本来就是卡片），工具参数/结果在卡片内横向滚动 | 同左 | 同左 |
| 设置 `SettingsPage` | 标题与"新增接入"**分两行**；模型列表是**卡片**（类型 / 上下文 / 最大输出 / Tokenizer 各带标签） | 640 起"当前生效的模型"是两列；模型列表仍是卡片 | 一行两端对齐 + 六列表格 |
| 用量 `UsagePage` | 三个合计数字**竖排**；按模型的表是**卡片**（调用 5 次 · 输入 3,263 · 输出 225） | 640 起三个合计数字一行；按模型的表仍是卡片 | 三数字一行 + 五列表格 |
| 引导 `OnboardingPage` | 单列表单（本来就是一张卡片，`max-w-xl`） | 同左 | 同左 |
| 404 `NotFoundPage` | 居中一列 | 同左 | 同左 |

#### 表格的降级路径：成对渲染，不用 JS 判断断点

三张表（文档列表、模型表、用量表）都是**同一个结构**：一份 `<table>` 加一份卡片列表，
用 `lg:hidden` / `hidden lg:table` 切换，重复的内容抽成行内小件（`FilenameCell` /
`StatusCell` / `ModelCell` / `ModelRowActions` / `KindBadge`）。

- **为什么不用 `matchMedia` 选一个渲染**：首帧必然是初始分支，effect 跑完才换——
  窄屏上会先闪一下六列表格。两套都在 DOM 里、由 CSS 决定谁出现，没有这一帧。
- **代价（要如实知道）**：jsdom 不解析 Tailwind 类名，两套都在测试的 DOM 里，
  所以按文件名 `getByText` 会撞上两个节点；`KnowledgeBaseDetailPage.test.tsx` 里
  相应的查询改成了 `findAllBy*` / `within(表格)`，并写明理由。
- **为什么阈值是 `lg:`（1024）而不是 `sm:`（640）**：这是实测量出来的。六列表格的
  min-content 宽度是 510px（文档）/ 581px（模型），还要加侧栏展开的 256px 和页面
  `p-6` 的 48px。640 和 768 都排不下——**768px 下实测仍溢出**（文档列表
  `scrollWidth 816 > 768`，设置页 `887 > 768`）。这两处溢出在改动之前就存在
  （旧构建上量出来的数字一模一样），只是第一次测量只覆盖了 390px。

#### 引用角标：点击是悬停的等价物

`CitationBadge` 是**受控**的 `HoverCard`（同一个 `open` / `onOpenChange`），触发器是
`<sup>` 里包的真 `button`：悬停、聚焦（键盘 Tab）、点击三条路写同一个状态。
不换成 `Popover` 是因为 `HoverCard` 在聚焦时本来就会开，换掉等于把桌面上的悬停体验
和键盘可达一起丢。跟三件事有关的实测：

- **触屏**：390px 下 `touchscreen.tap` 角标——改动前卡片数 `0`（打不开，正是这条
  issue 报的），改动后 `1`，再点一下回到 `0`。
- **命中区**：Tailwind 的 preflight 把 `sup` 设成 `line-height: 0`（"上标不影响行高"），
  角标的盒子高度实测是 **0px**。0 高的盒子没有命中面积，能点中靠的是文字的字形盒
  （坐标点上去命中的是 sup 的文字，宽 10px、高 9px）——做触屏目标太小。所以给
  `sup` 一个 `leading-none`、给按钮前后 2px/上下 6px 的 padding（负 margin 把占位
  扣回去，排版一个像素没动），命中区变成 21px 高，`elementFromPoint` 返回的就是
  这个 button 自己。
- **引用只在流式那条气泡上存在**：契约里消息没有 `citations` 字段、引用不落库，
  所以验证它必须趁流还在跑（等流结束，角标整块消失）。

### 16. 每个异步动作都要有完整反馈

§2 讲的是页面渲染哪几种状态，这一条讲"用户怎么知道事情在发生"：

- **加载**：按钮要 `disabled` 并改文案（"保存并开始" → "探测中…"），防重复提交；列表区放骨架，不要留白。
- **成功**：下一步要能看见结果——写操作靠 `invalidateQueries`（§3），别只弹一个 toast 报喜。
- **失败**：形式由 `lib/errors.ts` 的 `errorPresentation()` 决定，**不要在页面里自己判断**。写操作失败走 toast（`useErrorToast`），查询失败留在页内 `Alert`。
- **空**：和加载态分开（§2）。
- 「加载更多」在没有下一页时**整个不渲染**，不要渲染成禁用——禁用会让人以为等一下就有了。
- 判据：任何一个返回 Promise 的操作，界面上都找得到"正在做 / 做完了 / 失败了"三种可见证据。

### 17. AI / Agent 界面要暴露什么

- run 状态（`pending / running / completed / failed / cancelled / interrupted`）要有**统一的中文映射表**，不要在每个组件里各写一份（先例：`AgentDetailPage.tsx` 的 `STATUS_LABEL`）。
- 流式过程中必须看得见"还在生成"：正文末尾的光标块（`MessageBubble.tsx` 的 `pending`）。
- 工具调用要显示工具名 + 在跑还是跑完了；参数与结果默认收起（`ToolCallCard.tsx`）。
- 失败要说清是**哪一轮**失败，"这一轮"的记录留在页面上（流式失败不 toast，理由在「已经做完的」一节）。
- 现状：停止按钮（#79）、轨迹页的工具折叠卡片（#80）、Markdown（#87）、复制（#89）、重试（#90）都还没有。**别因为这一节就把它们当成已实现。**

### 18. RAG 界面的文档状态

- 文档行要能回答三个问题：它是什么（文件名）、它现在怎么了（状态）、我能做什么（重新索引 / 删除）。
- 状态与后端的文档状态机一一对应（`queued / processing / ready / failed`），前端**不自己发明状态**，也不自己判断"这个状态能不能重试"。
- 「处理中」必须有"还在动"的指示（旋转图标 + 文字，见 `DocumentStatusBadge.tsx`），只给一个图标等于没说。
- 换 embedding 模型这类会清空向量的操作，用确认框解释代价（`OnboardingPage` 的 `AlertDialog`），不要做成一条红字报错——前者是"请你确认"，后者是"你做错了"。
- 判据：新增一个文档状态时，`DocumentStatusBadge` 是唯一要改的地方。

### 19. 表格与列表

- 表头写清列名；每行的操作按钮要有可访问名，并且带上对象名。
- 每行最多一个主操作，其余进溢出菜单——**不要在每行堆一排按钮**（文档行的"重新索引 / 删除"已经是上限）。
- 分页统一用「加载更多」按钮，不做滚动自动加载（结论见「还没做的」）。
- 排序与过滤（#92）还没有；做的时候要连带给出"筛完没有结果"的空态——它和"这里本来就没有东西"不是同一个空态。
- 窄屏下表格要有降级路径（表格转卡片），见 §15。

### 20. 弹窗、抽屉、页面：选哪个

- **破坏性 / 不可逆** → `AlertDialog`（`role="alertdialog"`，默认聚焦在取消上）。先例：`DeleteKnowledgeDialog.tsx`，文件注释里写了为什么不是 `Dialog`。
- **需要填字段** → `Dialog`。
- **侧边的次要内容** → `Sheet`（抽屉）。移动端导航用它，不要把桌面侧栏硬缩成一条。
- 弹窗里的表单靠**换 `key`** 重置（§8）；字段级的失败文案贴在字段旁（`errorPresentation` 返回 `inline` 的那种）。
- 判据：破坏性操作永远不用 `Dialog`；弹窗的第一行必须是标题（`AlertDialogTitle` / `DialogTitle`），不能只有描述。

### 21. 动画与减弱动态

- 动画只用来表达"状态变了"（进出场、进行中、追加中），不做装饰性动效。
- **必须尊重系统的「减弱动态效果」**：`src/index.css` 里那条 `@media (prefers-reduced-motion: reduce)` 是全局的，`animation-duration` 压到 0.01ms 且只跑一遍。它**必须留在 `@layer` 外面**——Tailwind 的工具类在 `@layer utilities` 里，分层样式比的是层不是选择器优先级，放进 `@layer base` 会被工具类压住、静默失效。
- **压停不等于删掉**，状态必须静止可辨。逐条判据：
  - 靠旋转图标表达"进行中"的，旁边必须有文字（"处理中""运行中…"）——图标停住、文字还在；
  - 流式光标是唯一"只靠动画表达"的元素，压停后停在**实心块**上（`.animate-pulse` 那条单独规则）；
  - 骨架屏压停后是静止的灰块，仍然看得出"这里是空的"；
  - 弹窗 / 抽屉 / 下拉靠 `animationend` 卸载（Radix 的 Presence），所以只能压时长、**不能写 `animation: none`**；
  - toast 的降级由 sonner 自己处理（它对 toast 直接 `animation/transition: none`）。
- 新增任何"用动画表达状态"的元素时，回到上面这几条问一句：**把动画关掉之后，状态还在不在。**
- 工具上怎么确认：`tw-animate-css` 的 `.animate-in` / `.animate-out` 最终写的是 `animation` 属性，主题变量在 `node_modules/tw-animate-css/dist/tw-animate.css`。读它，别凭记忆——shadcn 的弹窗、抽屉、下拉全都靠它。
- 手工验收不用改系统设置：Chrome DevTools → Rendering → **Emulate CSS media feature `prefers-reduced-motion`** 选 `reduce`，然后过一遍主要交互（开弹窗、删知识库、发一条消息、等文档从"处理中"变"就绪"），逐条对上面的判据看状态是不是还认得出来。

### 已达标项（判据在这里，别再重复审计）

| 项 | 判据 | 怎么复核 |
| --- | --- | --- |
| 零硬编码颜色 | 业务代码里没有 `#hex` / `rgb()` / `hsl()` 字面量，颜色只用语义 token（`bg-background` / `text-muted-foreground` …）或带 `dark:` 变体的调色板类 | 在 `web/src` 里搜色值字面量（`#` 开头、`rgb(`、`hsl(`），排除 `components/ui/`——**字面量为零**；非语义 token 的调色板类有两处（`DocumentStatusBadge` 的 `emerald`/`sky`，都带 `dark:` 变体）。新代码请用语义 token，别再加第三处 |
| 破坏性操作走 `AlertDialog` | 知识库删除是 `AlertDialog`，注释写明为什么不是 `Dialog` | `components/knowledge/DeleteKnowledgeDialog.tsx` |
| `role="alert"` 与 `aria-live` 的分工 | `ErrorToast` 用 `role="alert"` 且**不**同时写 `aria-live`；`PageFallback` 用 `role="status"` + `aria-busy` | `ErrorToast.test.tsx` 有断言，两个组件都有注释 |
| `<html lang="zh-CN">` 与深色优先 | `index.html` 写死 `lang="zh-CN"` + `class="dark"`，防闪白脚本同步执行、排在 `main.tsx` 之前 | 读 `web/index.html` 与 §10 |
| 路由切换后的焦点管理 | `RouteFocus` 聚焦新页面的 `h1`，落点不进 Tab 序列、没有聚焦框，且不抢 `autoFocus` | `AppLayout.test.tsx` 的四条焦点用例 |
| 整页只有一个 `main` 地标 | 内容容器是 `div[tabindex="-1"]`，唯一的 `<main>` 来自 `SidebarInset` | `AppLayout.test.tsx` 的"整页只有一个 main 地标" |
| 减弱动态效果 | 全局 `prefers-reduced-motion` 规则 + 流式光标兜底 | 系统里打开"减弱动态效果"，逐条对 §21 |
| 文档状态只有一个映射点 | `DocumentStatusBadge` 的四态与后端状态机一一对应 | `components/knowledge/DocumentStatusBadge.tsx` |
| 窄屏下的侧栏是抽屉 | `<768px`（`use-mobile.ts` 的断点）点 `button[data-sidebar="trigger"]`，`[role="dialog"]` 从 0 变 1 | 390px 下实测过，见 §15 |
| 引用展开有手指的等价物 | `CitationBadge` 是受控 `HoverCard` + 真 `button` 触发器；触屏点一下开、再点一下关 | `CitationBadge.test.tsx`；390px 下实测过，见 §15 |

**审计发现、但本批次不修的缺口**（记在这里，免得下一轮重新发现一遍）：

- `ConversationPage` / `AgentDetailPage` 的发送按钮只有图标、没有 `aria-label`；
- 文档行上的删除按钮直接执行，没有确认框（属 issue #91）。

---

## 新增一个页面

1. `src/pages/XxxPage.tsx` — 默认导出，页面里要有且只有一个 `h1`（§12；`headingStructure.test.ts` 会检查，真要例外就把它写进那个文件的清单并说明理由）
2. 在 `src/hooks/` 加数据 hook（不要直接在页面里调 `api.*`）
3. 在 `src/router.tsx` 的布局路由下加一条 `<Route path="xxx" element={<XxxPage />} />`
4. 在 `src/layouts/nav.ts` 的 `NAV` 里加入口；还没实现的模块加 `soon: '尚未实现'`，会渲染成禁用项（**写事实，不要写里程碑编号**——编号会过期，见 `nav.ts` 里的注释）
5. 接口变了先改 `contracts/openapi.yaml` 再 `make generate`
6. `make check` 确认类型、构建、lint 都过

---

## 常用命令

```bash
# 在仓库根目录
make check          # go build + vet + test + 前端 lint（提交前跑这个）
make build          # 前端构建 → apps/api/web/ → go build
make build-web      # 只构建前端
make dev            # 起 api（前端由 Go 内嵌提供，:3210）
make generate       # 契约 → Go 代码 + TS 类型

# 在 web/ 目录
pnpm dev                          # Vite dev server（:5173，有 HMR）
pnpm build                        # tsc -b && vite build
pnpm lint                         # oxlint
npx --yes shadcn@latest add <组件>  # 加 shadcn 组件
```

**改前端代码时用 `pnpm dev`（:5173）**，改完立刻生效。

**`:3210` 是构建产物**，改了源码不重新 build 不会变。两个端口都能开，但它们不是同一份东西。

**加 shadcn 组件之后要跑一次 `make check`**——生成的组件可能带 lint 告警。`src/components/ui/` 和 `src/hooks/use-mobile.ts` 已经在 `.oxlintrc.json` 的 `ignorePatterns` 里，新增的文件如果也有告警，加进去（那是生成代码，改了会被覆盖）。

---

## 已知的坑

这些都是实际踩过的，不是从文档抄的。

| 坑 | 现象 | 原因 |
| --- | --- | --- |
| `pnpm dlx shadcn@latest` **必崩** | `ERR_PACKAGE_PATH_NOT_EXPORTED ... zod/package.json` | CLI 4.x 内置 MCP server，它的依赖在 pnpm dlx 的 hoisting 下解析不到 zod 的 `./v4` 导出。**用 `npx --yes shadcn@latest`** |
| tsconfig 里写 `baseUrl` **硬报错** | `error TS5101: Option 'baseUrl' is deprecated` | TypeScript 6.0 弃用了它。`paths` 不带 `baseUrl` 时按本文件位置解析，所以写 `"./src/*"` |
| `paths` 只放根 tsconfig **无效** | `Cannot find module '@/lib/utils'` | 根 tsconfig 是 `files: []` + references，`tsc -b` 用被引用项目自己的配置。必须写进 `tsconfig.app.json` |
| 类型导入不写 `import type` | `tsc -b` 报错，`pnpm build` 第一步就挂 | `verbatimModuleSyntax: true` 的要求 |
| `import { RouterProvider } from 'react-router'` | 用到 `flushSync` / `viewTransition` 时才失效并告警 | 必须从 `react-router/dom` 导入。**我们没用 RouterProvider，但记住这条** |
| `cssMinify: 'esbuild'` | `Cannot find package 'esbuild'` | Vite 8 底层换成了 rolldown，不再自带 esbuild。别照抄旧教程的这行配置 |
| `react-router-dom` | 装进来一个 v7 副本，报 `useNavigate() may be used only in the context of a <Router>` | v8 里这个包**已删除**，一切都从 `react-router` 导入 |
| 侧栏被主区域撑变形 | 数据一多就出现，数据少时看不出来 | grid/flex 子项默认 `min-width: auto`。主区域必须 `min-w-0` |
| `SidebarProvider` 里用 tooltip | 报 Provider 缺失 | 它**不内置** `TooltipProvider`，要在外层自己包一个（见 `main.tsx`） |
| 数组字面量加 `as const` 后可选字段推不出 | `Property 'soon' does not exist on type ...` | `as const` 让每一项成了独立类型。给数组显式类型 `NavItem[]` 代替 |
| 在弹窗里用 `useEffect` 重置输入框 | `oxlint` 报 `react(set-state-in-effect)` | 换 `key` 让组件重新挂载，见 §8 |
| `sup` 里的东西点不中 | 看着在、坐标点上去命中的是文字而不是那个元素；盒子高度实测 **0px** | Tailwind 的 preflight 有 `sup, sub { line-height: 0 }`（"上标不影响行高"）。要给它一个 `leading-none`，见 §15 的引用角标 |
| 全新克隆后前端是白屏 | — | **已修**：`.gitignore` 现在整目录忽略 `apps/api/web/`（只留 `.gitkeep`），不再提交指向 hash 产物的 `index.html`。没构建前端时 `MountSPA` 会返回明确的提示页 |

**关于 `components.json` 的 `style`**：官方文档说"初始化后不能改"，但 CLI 4.21 有 `shadcn apply [preset]`（改主题/字体）和 `shadcn migrate`（逐组件迁移，如 `migrate accordion to base-ui`）两个子命令。选错了不一定要删库重来，先看这两个。

---

## 已经做完的（v3.0）

原来这张表列的四条，v3.0 全做完了，留在这里是为了让后来者知道**判据在哪**：

| 项 | 现在的状态 |
| --- | --- |
| 前端自动化测试 | vitest + jsdom + Testing Library，CI 的 web job 会跑 `pnpm --filter web test`。9 个 hook 里 `useTheme` 那条是真回归测试（先红后修）；其余几条是**契约固定测试**，钉的是缓存 key 与响应形状，不是缺陷回归——别把它们当"修复前会失败"的证据 |
| 写操作的失败提示 | 写操作失败走 **toast**；**查询失败与流式生成失败保留页内 Alert**（前者失败后页面本来就没内容，后者是"这一轮失败了"的持久记录）。判据落在 `lib/errors.ts` 的 `errorPresentation()` 里，不要在页面里自己判断 |
| 按路由拆包 | 6 个页面 `React.lazy`，另加 `RouteErrorBoundary`（懒加载 chunk 失败时给"刷新页面"按钮，而不是白屏）。落地页与 404 页保持静态导入 |
| 静态资源缓存头 | `assets/` 下是 immutable，`index.html` 与 favicon 是 `no-cache`，错误响应是 `no-store`（见 `apps/api/internal/api/spa.go`） |

## 还没做的

| 项 | 说明 |
| --- | --- |
| 无限滚动 | **issue #84 的结论：继续用「加载更多」按钮，不做滚动自动加载。** v4.0 把这三个列表都重做过（会话列表新建、文档列表补了状态与分块数、运行历史补了排序筛选与折叠卡片），所以当初那句"收益不抵成本"的成本结构已经变了，因此重估了一次。结论仍然是按钮，三条理由：① **文档列表是表格**，它的滚动容器与"到底了"的判定和卡片列表不是一回事，两条路要分别实现；② **会话消息是升序列表**（最新在底部），自动加载的方向感与两个倒序列表相反，而它已经有"滚到底部"这个行为，两者叠在一起会互相抢滚动；③ 自动加载需要 `IntersectionObserver` 加一道"正在加载时别在同一个哨兵上重复触发"的闸，而按钮天然不会重复触发。**代价如实记着**：用户要多点一次。三个列表的交互是一致的——同一套 `<Button variant="ghost" size="sm">`、没有下一页时**整个不渲染**（禁用会让用户以为"等一下就有了"）、加载中改文案「加载中…」。位置随排序方向走（升序的在顶部、两个倒序的在底部），那不是不一致，方向本身就相反 |
| 响应式 | issue #86 已做：逐页的窄屏策略与三档宽度写在 §15，三张表有卡片降级路径，引用角标补了点击。**还有一处没做**：`640–1023` 这一档三张表仍然是卡片（六列表格放不下，实测），没有专门为平板调过列宽 |
| 会话列表的「当前会话高亮」 | `/conversations` 是一页独立列表，列表里没有"正在看的那一个"。要做到得给 `conversations/:id` 套一层"左列表 + `<Outlet/>`"的布局壳——那会动 `AppLayout`/路由结构，当时没做（issue #78 的验收里这一条未达成） |
| Agent 的「绑定模型」 | 后端**没有** per-agent 的模型绑定：一次运行用的是全局当前生效的 chat 模型（`LatestByKind` 决定）。设置页只读展示它，表单里没有这一项（issue #81） |
| provider 的编辑 / 删除 | 契约里 `/api/v1/providers` 只有 `GET` / `POST`——换 Key 用 `POST` 覆盖即可，"删掉一个接入点"会连带删掉它下面的模型与用量记录，所以没有提供（issue #83） |

---

## 和契约的关系

```text
contracts/openapi.yaml
        │
        ├─ oapi-codegen ──────→ apps/api/internal/api/generated.go（Go）
        └─ openapi-typescript → packages/api-client/src/schema.d.ts（TS）
                                        │
                                        ▼
                              @congorag/api-client 的 api 对象
                                        │
                                        ▼
                                  hooks/useXxx.ts
                                        │
                                        ▼
                                    pages/
```

**改接口的顺序**：先改 `contracts/openapi.yaml` → `make generate` → 两边的类型一起变 → 编译/类型检查会告诉你哪里要跟着改。

不要在前端绕过这份契约自己拼请求。

---

## 这个技术栈上，判断依据的优先级

```text
node_modules 里的实际源码 / .d.ts      ← 最可信
官方 changelog / release notes
官方文档                              ← 可能滞后于刚发布的版本
博客 / StackOverflow / AI 生成的代码   ← 最不可信
```

实测过的例子：`react-router` 的 Context7 文档索引最高只到 7.18.2（没有 v8），而 `node_modules/react-router@8.4.0` 的类型定义是准的；shadcn 官方给的 tsconfig 片段里含 `baseUrl`，在 TypeScript 6.0 下是硬报错。

**能读源码就别读文档，能跑一遍就别信描述。**
