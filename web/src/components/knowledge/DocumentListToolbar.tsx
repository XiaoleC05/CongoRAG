import { DOCUMENT_FILTERS, DOCUMENT_SORTS, FILTER_LABEL, SORT_LABEL } from '@/hooks/useDocuments'
import type { DocumentFilter, DocumentSort } from '@/hooks/useDocuments'
import { cn } from '@/lib/utils'

type Props = {
  sort: DocumentSort
  filter: DocumentFilter
  onSortChange: (sort: DocumentSort) => void
  onFilterChange: (filter: DocumentFilter) => void
  /** 已经加载到客户端的文档数（不是知识库里的总数，见下面"已加载"文案的说明） */
  loadedCount: number
}

/**
 * 文档列表的筛选 / 排序工具条（issue #92）。
 *
 * 【为什么是一排开关按钮，而不是两个下拉】
 *   1. 取值是固定的几个（状态 5 个、排序 3 个），摊开一共两行就到，
 *      点一次完成；收进下拉要点两次，还要记住"上次选的是哪个"——
 *      工具条上的开关自己就写着当前状态。
 *   2. `aria-pressed` 让读屏软件直接念出"已按下"，不需要用户展开一个下拉
 *      才知道现在是筛着还是没筛。
 *   3. 【测试上的硬约束，如实写在这里】shadcn 的 Select 在 jsdom 下打不开：
 *      它的触发器要调 `hasPointerCapture`，而 jsdom 没实现这个方法，实测报
 *      `target.hasPointerCapture is not a function`，用例连展开这一步都到不了。
 *      DropdownMenu 能打开，但**一个测试文件里只能打开一次**（`@radix-ui/react-menu`
 *      的模块级状态；换新 root、清空 document.body 都不管用，都实测过）。
 *      工具条上的两个控件如果是下拉，就吃掉了那个唯一的名额——行尾的溢出菜单
 *      （issue #91 要求必须有的）会变得测不了。换成开关按钮，两边都能测。
 *
 * 【"已加载 N 份"不是装饰】排序与筛选都在客户端做（理由见 useDocuments.ts 的
 * sortAndFilterDocuments），**没翻过的页不参与**。不写这句，用户会把
 * "筛不出来"当成"本来就没有"——而后者正是 issue #92 要求区分开的那个空态。
 * 右对齐（ml-auto）是为了不和左边的筛选控件抢同一条视线。
 */
export function DocumentListToolbar({
  sort,
  filter,
  onSortChange,
  onFilterChange,
  loadedCount,
}: Props) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2">
      <ToggleGroup label="按状态筛选">
        {DOCUMENT_FILTERS.map((f) => (
          <Chip key={f} pressed={filter === f} onClick={() => onFilterChange(f)}>
            {FILTER_LABEL[f]}
          </Chip>
        ))}
      </ToggleGroup>

      <ToggleGroup label="排序">
        {DOCUMENT_SORTS.map((s) => (
          <Chip key={s} pressed={sort === s} onClick={() => onSortChange(s)}>
            {SORT_LABEL[s]}
          </Chip>
        ))}
      </ToggleGroup>

      {/* 【"一键清除筛选"的按钮不在这里】它归"筛完没有结果"那个空态
          （见 KnowledgeBaseDetailPage 的 NoMatchState）。放两处是同一个动作
          两个同名按钮，读屏软件的顺序浏览会撞见两个"清除筛选"，而他并不知道
          该点哪个。筛着但有结果时，清除就是点回"全部"——那个开关自己写着
          aria-pressed，也已经说明了现在筛的是什么。 */}

      <span className="text-muted-foreground ml-auto text-xs">已加载 {loadedCount} 份</span>
    </div>
  )
}

/**
 * 一组开关按钮的容器。
 *
 * 【为什么要 role="group" + aria-label】单个按钮的文案是"全部""就绪"这种短词，
 * 单独念出来不知道在说什么；分组标签把"这一排是干什么的"补齐，而且读屏软件
 * 进组时会先播报它。用 group 而不是 toolbar：这里没有方向键导航那套约定，
 * 声明成 toolbar 反而给了用户一个不存在的期待（Tab 在按钮之间走是正常的）。
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
 * 【为什么不用 shadcn 的 Button】Button 是"动作"，这里是"状态"：
 * 需要 aria-pressed 和一套"选中/未选中"的样式，塞进 Button 的 variant 表里
 * 会让那张表同时表达两件事（动作类型 + 选中态）。这正是 §12 说的
 * "层级是标签的事，字号是排版的事"的同一类判断——语义归语义，样式归样式。
 *
 * 【键盘可聚焦 + 焦点可见】它就是原生 button，Tab 能到；焦点框沿用
 * shadcn 组件那套 ring。**不写 `outline-none`**（§14：可操作元素把默认焦点框
 * 去掉就必须自己补一个更明显的，否则"焦点在哪"就没人看得见）。
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
