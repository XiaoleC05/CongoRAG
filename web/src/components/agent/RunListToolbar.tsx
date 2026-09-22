import { RUN_FILTER_LABEL, RUN_FILTERS, RUN_SORTS, RUN_SORT_LABEL } from '@/hooks/useAgents'
import type { RunFilter, RunSort } from '@/hooks/useAgents'
import { cn } from '@/lib/utils'

type Props = {
  sort: RunSort
  filter: RunFilter
  onSortChange: (sort: RunSort) => void
  onFilterChange: (filter: RunFilter) => void
  /** 已经加载到客户端的 run 数（不是这个 Agent 跑过的总数，见下面"已加载"文案） */
  loadedCount: number
}

/**
 * 运行历史的筛选 / 排序工具条（issue #92 的另一半）。
 *
 * 【为什么和文档列表的工具条长得一样】同一套交互只应该有一套。文档列表
 * （components/knowledge/DocumentListToolbar.tsx）先落地了"一排开关按钮"的
 * 做法，这里是照着它做的**同一个交互**，理由完全相同：
 *
 *   1. 取值是固定的几个（状态 7 个含"全部"、排序 2 个），摊开就到，点一次
 *      完成；收进下拉要点两次，还要记住"上次选的是哪个"；
 *   2. `aria-pressed` 让读屏软件直接念出"已按下"，不必先展开一个下拉；
 *   3. 【测试上的硬约束，如实写】shadcn 的 Select 在 jsdom 下打不开：它的触发器
 *      要调 `hasPointerCapture`，jsdom 没实现，实测报 `target.hasPointerCapture
 *      is not a function`，用例连展开那一步都到不了。DropdownMenu 能开，但
 *      一个测试文件里只能开一次（`@radix-ui/react-menu` 的模块级状态），
 *      而这一页的行里没有别的菜单要开——不过"两个列表用同一套控件"本身
 *      就是更重要的理由，不该为了第二个列表换控件。
 *
 * 【这份 Chip/ToggleGroup 是复制来的，不是抽出来的】README 的判据是
 * "等第二个页面真的要用了再抽"——现在确实是第二个了，但抽成
 * `components/` 根下的通用组件会动到知识库那一侧的文件（本批次不动它），
 * 所以这里先复制一份，并把差异点（多一档筛选、没有"每行操作"那类约束）
 * 写在注释里。**第三个列表要用的时候，把这一份和 knowledge 那一份一起
 * 抽到 components/ 根下**——两处各自长下去就会开始漂。
 *
 * 【"一键清除筛选"的按钮不在这里】它归"筛完没有结果"那个空态
 * （见 AgentDetailPage 的 RunNoMatchState）。放两处就是同一个动作两个同名
 * 按钮，读屏软件顺序浏览时会撞见两个"清除筛选"而不知道该点哪个。
 * 筛着但有结果时，清除就是点回"全部"——那个开关自己写着 aria-pressed。
 */
export function RunListToolbar({
  sort,
  filter,
  onSortChange,
  onFilterChange,
  loadedCount,
}: Props) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2">
      <ToggleGroup label="按状态筛选">
        {RUN_FILTERS.map((f) => (
          <Chip key={f} pressed={filter === f} onClick={() => onFilterChange(f)}>
            {RUN_FILTER_LABEL[f]}
          </Chip>
        ))}
      </ToggleGroup>

      <ToggleGroup label="排序">
        {RUN_SORTS.map((s) => (
          <Chip key={s} pressed={sort === s} onClick={() => onSortChange(s)}>
            {RUN_SORT_LABEL[s]}
          </Chip>
        ))}
      </ToggleGroup>

      {/* 【"已加载 N 条"不是装饰】排序与筛选都在客户端做（理由见
          useAgents.ts 的 sortAndFilterRuns），**没翻过的页不参与**。
          不写这句，用户会把"筛不出来"当成"本来就没跑过失败的运行"——
          而后者正是 issue #92 要求区分开的那个空态。
          右对齐（ml-auto）是为了不和左边的筛选控件抢同一条视线。 */}
      <span className="text-muted-foreground ml-auto text-xs">已加载 {loadedCount} 条</span>
    </div>
  )
}

/**
 * 一组开关按钮的容器。
 *
 * 【为什么是 role="group" + aria-label】单个按钮的文案是"全部""失败"这种短词，
 * 单独念出来不知道在说什么；分组标签把"这一排是干什么的"补齐，读屏软件进组时
 * 会先播报它。用 group 而不是 toolbar：这里没有方向键导航那套约定，声明成
 * toolbar 反而给了用户一个不存在的期待（Tab 在按钮之间走是正常的）。
 */
function ToggleGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-1">
      <span className="text-muted-foreground text-xs">{label}</span>
      {children}
    </div>
  )
}

/**
 * 一个开关按钮。
 *
 * 【为什么不用 shadcn 的 Button】Button 是"动作"，这里是"状态"：需要
 * aria-pressed 和一套"选中/未选中"的样式，塞进 Button 的 variant 表里会让
 * 那张表同时表达两件事（动作类型 + 选中态）。
 *
 * 【键盘可聚焦 + 焦点可见】它就是原生 button，Tab 能到；焦点框沿用 shadcn
 * 组件那套 ring。**不写 `outline-none`**（§14：可操作元素把默认焦点框去掉
 * 就必须自己补一个更明显的，否则"焦点在哪"就没人看得见）。
 */
function Chip({
  pressed,
  onClick,
  children,
}: {
  pressed: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        'rounded-full border px-2.5 py-0.5 text-xs transition-colors',
        'focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3',
        pressed
          ? 'border-primary bg-primary/10 text-foreground'
          : 'border-border text-muted-foreground hover:bg-muted hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}
