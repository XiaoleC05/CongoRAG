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
    ├── layouts/            布局路由的组件（AppLayout）
    ├── pages/              一个路由一个文件，默认导出
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
| `embedding_change_requires_reindex` | 409 | **换 embedding 模型需要先确认清空重建**——引导页按它弹确认框，用户确认后带 `allowEmbeddingReset` 重发 |
| `upstream_llm_error` | 502 | 上游模型服务出错 |
| `context_overflow` | 400 | 上下文超出模型窗口（M3 起） |
| `internal_error` | 500 | 服务内部错误，详情在服务端日志 |

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

## 新增一个页面

1. `src/pages/XxxPage.tsx` — 默认导出
2. 在 `src/hooks/` 加数据 hook（不要直接在页面里调 `api.*`）
3. 在 `src/router.tsx` 的布局路由下加一条 `<Route path="xxx" element={<XxxPage />} />`
4. 在 `src/layouts/AppLayout.tsx` 的 `NAV` 里加入口；还没实现的模块加 `soon: 'Mx'`，会渲染成禁用项
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
| 无限滚动 | 三个分页列表统一用"加载更多"按钮而不是滚动自动加载。对话页已有"滚到底部"的行为、文档页是表格布局，两处都要重做，收益不抵成本 |
| 会话列表端点 | 契约里没有 `GET /api/v1/conversations`（只有 `post:`），所以侧栏列不出会话。`useConversations.ts` 的注释里记着这件事 |

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
