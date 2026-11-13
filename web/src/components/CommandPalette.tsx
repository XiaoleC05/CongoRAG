import { BookOpen, Bot, MessagesSquare, Moon, Sun } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { LucideIcon } from 'lucide-react'
import { useNavigate } from 'react-router'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAgents } from '@/hooks/useAgents'
import { useConversations } from '@/hooks/useConversations'
import { useKnowledgeBases } from '@/hooks/useKnowledgeBases'
import { useTheme } from '@/hooks/useTheme'
// 页面导航表直接从布局层取：命令面板的"页面"组必须和侧栏是同一份数据，
// 否则加一个页面却忘了加进面板，就是"入口在侧栏里、搜不到"的静默不一致。
import { NAV } from '@/layouts/nav'
import { cn } from '@/lib/utils'

/**
 * 全局命令面板（⌘K / Ctrl+K）。
 *
 * 【为什么需要它】这个应用有 6 个页面 + 三类实体（知识库/会话/Agent），
 * 找东西的路径全是"先回列表页、再翻、再点"。命令面板把"我要去 X"
 * 压缩成一次输入；⌘K 是这类产品里用户已经形成肌肉记忆的入口。
 *
 * 【为什么挂在 AppLayout 而不是每个页面各挂一个】快捷键要在**任何页面**
 * 都能用；挂在布局路由上只注册一次监听，切页也不会重建。
 *
 * 【为什么数据 hooks 放在 PaletteBody 里、且只在打开时才挂载】
 * Dialog 关闭时 Radix 根本不渲染 Content，PaletteBody 因此不挂载——
 * 知识库/会话/Agent 三个列表查询也就不会在每次打开页面时被"顺手拉一遍"。
 * 面板打开时这三个 hook 读的都是 TanStack Query 的缓存（页面大多已经拉过），
 * 不额外增加首屏请求。
 *
 * 【为什么不做"搜索所有消息内容"】那需要全文检索端点；现在拿不到就是拿不到，
 * 做成本地过滤已加载的消息会给出"搜不到 = 不存在"的假结论。面板只搜
 * **导航目标与实体名**（知识库名、会话标题、Agent 名），范围写在 placeholder 里。
 */

type PaletteItem = {
  id: string
  group: GroupName
  label: string
  hint?: string
  icon: LucideIcon
  /** 过滤用的文本（标签 + 路径/id），小写化在 fuzzyMatch 里做。 */
  searchText: string
  run: () => void
}

const GROUP_ORDER = ['页面', '知识库', '会话', 'Agent', '动作'] as const
type GroupName = (typeof GROUP_ORDER)[number]

/** 每组最多显示多少条：空查询时按组截断，避免"会话多"把页面导航挤出视野。 */
const GROUP_LIMIT = 5

/**
 * 子序列模糊匹配（大小写不敏感）。
 *
 * 【为什么是子序列而不是 includes】"zl" 能命中"知识库列表"这种中文缩写场景、
 * 也能命中路径片段；includes 要求连续匹配，实际用起来会很挫。
 * 代价是误命中更多——对命令面板来说"多给一条"比"搜不到"好。
 */
function fuzzyMatch(text: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '') return true
  const t = text.toLowerCase()
  let i = 0
  for (const ch of t) {
    if (ch === q[i]) {
      i += 1
      if (i === q.length) return true
    }
  }
  return i === q.length
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * 是否由组件自己注册 ⌘K/Ctrl+K。
   *
   * 【默认 true，AppLayout 里传 false】面板是懒加载 chunk：chunk 还没到时组件
   * 根本没挂载，快捷键也就注册不上——用户首屏后就按 ⌘K 会遇到"没反应"。
   * 所以真实应用里由布局层（一定已加载）注册快捷键，组件这里只在测试/独立
   * 使用时才自己注册。两处同时注册会切换两次（开了又关），所以要能关掉。
   */
  shortcutEnabled?: boolean
}

export function CommandPalette({ open, onOpenChange, shortcutEnabled = true }: Props) {
  // 打开前的焦点元素：关闭时要把焦点还回去（侧栏的搜索按钮 / 页面上的落点）。
  const openerRef = useRef<HTMLElement | null>(null)
  const wasOpen = useRef(false)

  useEffect(() => {
    if (open && !wasOpen.current) {
      openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    }
    wasOpen.current = open
  }, [open])

  // 【快捷键注册在 window 上】面板要能从任何页面被叫出来；侧栏按钮只是
  // 同一个动作的可见入口，不是唯一入口。
  useEffect(() => {
    if (!shortcutEnabled) return
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        onOpenChange(!open)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange, shortcutEnabled])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        // 不居中：命令面板从上方 1/8 处展开，像"从顶部拉下来的搜索"，
        // 同时给下方列表留出尽量多的高度。
        className="top-[12%] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-lg"
        showCloseButton={false}
        aria-modal="true"
        // 【关闭后焦点回到打开它的元素】没有 DialogTrigger（入口在侧栏/快捷键里），
        // Radix 不知道该还给谁，默认会落到 body——键盘用户按 Esc 后下一次 Tab
        // 会从页面开头重新走。这里显式还回去。
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          const target = openerRef.current
          if (target?.isConnected) target.focus()
        }}
      >
        <DialogTitle className="sr-only">命令面板</DialogTitle>
        <DialogDescription className="sr-only">
          搜索页面、知识库、会话与 Agent，或执行切换主题等动作。
        </DialogDescription>
        <PaletteBody onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

/**
 * 面板主体（输入框 + 分组列表）。
 *
 * 【为什么单独一层】它只用数据 hooks；拆出来之后"关闭即不挂载"能让三个
 * 列表查询跟着停掉（见文件头注释）。
 */
function PaletteBody({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const { theme, toggle } = useTheme()
  const { data: knowledgeBases } = useKnowledgeBases()
  const { data: conversations } = useConversations()
  const { data: agents } = useAgents()

  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement | null>(null)

  const listboxId = 'command-palette-listbox'

  // 面板一打开就聚焦输入框：用户按 ⌘K 之后直接打字，不该再点一下。
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const items = useMemo<PaletteItem[]>(() => {
    const navItems: PaletteItem[] = NAV.map((item) => ({
      id: `nav-${item.to}`,
      group: '页面',
      label: item.label,
      hint: item.to,
      icon: item.icon,
      searchText: `页面 ${item.label} ${item.to}`,
      run: () => navigate(item.to),
    }))

    const kbItems: PaletteItem[] = (knowledgeBases ?? []).map((kb) => ({
      id: `kb-${kb.id}`,
      group: '知识库',
      label: kb.name,
      hint: '知识库',
      icon: BookOpen,
      searchText: `知识库 ${kb.name} ${kb.id}`,
      run: () => navigate(`/knowledge-bases/${kb.id}`),
    }))

    // 只取第一页：命令面板是"回到刚才那个"的入口，不是会话管理器；
    // 为了搜更早的会话去拉全部分页，代价（网络 + 等待）比收益大。
    const conversationItems: PaletteItem[] = (conversations?.pages[0]?.items ?? []).map((conv) => ({
      id: `conv-${conv.id}`,
      group: '会话',
      label: conv.title,
      hint: '会话',
      icon: MessagesSquare,
      searchText: `会话 ${conv.title} ${conv.id}`,
      run: () => navigate(`/conversations/${conv.id}`),
    }))

    const agentItems: PaletteItem[] = (agents ?? []).map((agent) => ({
      id: `agent-${agent.id}`,
      group: 'Agent',
      label: agent.name,
      hint: 'Agent',
      icon: Bot,
      searchText: `Agent ${agent.name} ${agent.id}`,
      run: () => navigate(`/agents/${agent.id}`),
    }))

    const actionItems: PaletteItem[] = [
      {
        id: 'action-theme',
        group: '动作',
        label: `切换到${theme === 'dark' ? '浅色' : '深色'}主题`,
        icon: theme === 'dark' ? Sun : Moon,
        searchText: '动作 切换主题 深色 浅色 外观',
        run: toggle,
      },
    ]

    return [...navItems, ...kbItems, ...conversationItems, ...agentItems, ...actionItems]
  }, [agents, conversations, knowledgeBases, navigate, theme, toggle])

  // 过滤 + 分组 + 每组截断。index 在这里一次算好，渲染时不用再 indexOf
  // （那是 O(n²)，列表长了会在每次按键时白跑）。
  const groups = useMemo(() => {
    const matched = items.filter((item) => fuzzyMatch(item.searchText, query))
    let flat = 0
    const built: Array<{ name: GroupName; items: Array<{ item: PaletteItem; index: number }> }> = []
    for (const name of GROUP_ORDER) {
      // 【页面组不截断】它是一个封闭集合（数量 = 侧栏项数）。截断它等于
      // "某个侧栏能进的页面在面板里搜不到"，用户只能靠背路径——这不是
      // 截断想省的那点滚动。动态实体（知识库/会话/Agent）才需要每组截断。
      const limit = name === '页面' ? NAV.length : GROUP_LIMIT
      const inGroup = matched.filter((item) => item.group === name).slice(0, limit)
      if (inGroup.length === 0) continue
      built.push({ name, items: inGroup.map((item) => ({ item, index: flat++ })) })
    }
    return built
  }, [items, query])

  const flatItems = useMemo(() => groups.flatMap((group) => group.items.map((entry) => entry.item)), [groups])
  // 过滤后条目变少时，选中下标要夹回合法区间（否则 Enter 会执行一个不存在的项）。
  const safeIndex = flatItems.length === 0 ? -1 : Math.min(activeIndex, flatItems.length - 1)
  const activeId = safeIndex >= 0 ? `command-palette-option-${safeIndex}` : undefined

  // 键盘选中项滚进视野：列表可滚动，选中项可能在折叠线以下。
  useEffect(() => {
    if (!activeId) return
    const el = document.getElementById(activeId)
    if (el && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'nearest' })
  }, [activeId])

  const changeQuery = useCallback((value: string) => {
    setQuery(value)
    setActiveIndex(0)
  }, [])

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (flatItems.length > 0) setActiveIndex((safeIndex + 1) % flatItems.length)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (flatItems.length > 0) setActiveIndex((safeIndex - 1 + flatItems.length) % flatItems.length)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      const item = safeIndex >= 0 ? flatItems[safeIndex] : undefined
      if (item) {
        item.run()
        onClose()
      }
    }
  }

  const runItem = (item: PaletteItem) => {
    item.run()
    onClose()
  }

  return (
    <>
      <div className="border-border border-b p-2">
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          aria-label="搜索命令"
          aria-expanded
          aria-controls={listboxId}
          aria-autocomplete="list"
          aria-activedescendant={activeId}
          autoComplete="off"
          placeholder="搜索页面、知识库、会话、Agent…"
          className="placeholder:text-muted-foreground w-full bg-transparent px-2 py-2 text-sm outline-none"
          value={query}
          onChange={(event) => changeQuery(event.target.value)}
          onKeyDown={handleKeyDown}
        />
      </div>

      <div className="max-h-80 overflow-y-auto p-2">
        {flatItems.length === 0 ? (
          <p className="text-muted-foreground px-2 py-8 text-center text-sm">没有匹配的命令</p>
        ) : (
          <div id={listboxId} role="listbox" aria-label="搜索结果">
            {groups.map((group) => (
              <div key={group.name} role="group" aria-label={group.name} className="mb-1">
                <p className="text-muted-foreground px-2 py-1 text-[11px] font-medium" aria-hidden>
                  {group.name}
                </p>
                {group.items.map(({ item, index }) => {
                  const selected = index === safeIndex
                  return (
                    <div
                      key={item.id}
                      id={`command-palette-option-${index}`}
                      role="option"
                      aria-selected={selected}
                      className={cn(
                        'flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm',
                        selected && 'bg-accent text-accent-foreground',
                      )}
                      onMouseMove={() => setActiveIndex(index)}
                      // mousedown 阻止默认：点选项时输入框会先失焦、再触发 blur，
                      // 某些浏览器上会把面板一起关掉，点击就变成了"没反应"。
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => runItem(item)}
                    >
                      <item.icon className="size-4 shrink-0" />
                      <span className="truncate">{item.label}</span>
                      {item.hint ? (
                        <span className="text-muted-foreground ml-auto shrink-0 truncate text-xs">
                          {item.hint}
                        </span>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="text-muted-foreground border-border flex items-center gap-4 border-t px-3 py-2 text-[11px]">
        <span>↑↓ 选择</span>
        <span>Enter 执行</span>
        <span>Esc 关闭</span>
      </div>
    </>
  )
}
