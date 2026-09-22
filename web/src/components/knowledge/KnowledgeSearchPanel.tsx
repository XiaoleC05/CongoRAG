import { FileText, Search } from 'lucide-react'
import { useState } from 'react'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useKnowledgeSearch } from '@/hooks/useDocuments'

type Hit = Schemas['KnowledgeSearchHit']

/**
 * topK 的上下界。**和契约的 minimum / maximum 必须一致**（`
 * contracts/openapi.yaml` 的 KnowledgeSearchRequest.topK 是 1–50）。
 *
 * 【客户端这一份不夹取，只拦下】§6：客户端校验只是省一次白跑的往返，
 * 真正的强制在后端（超范围后端返 400）。把 80 悄悄改成 50 发出去，
 * 用户会以为"只命中这么多"——调试视图上这个区别很关键。
 */
const TOP_K_MIN = 1
const TOP_K_MAX = 50

/** 空串 = 不传 topK，用服务端默认值（契约里是 5）。 */
function validateTopK(raw: string): string | null {
  const s = raw.trim()
  if (s === '') return null
  const n = Number(s)
  if (!Number.isInteger(n) || n < TOP_K_MIN || n > TOP_K_MAX) {
    return `只能填 ${TOP_K_MIN}–${TOP_K_MAX} 之间的整数，留空用默认值`
  }
  return null
}

/**
 * 检索调试视图（Hit Testing，issue #77 的前端一半）。
 *
 * 【它解决的是什么问题】用户能看到的往往只有最终答案和引用角标，
 * "这个问题为什么答错了"完全无法自查——是没检索到、还是检索到了但排序不对、
 * 还是检索对了而生成跑偏？这个视图把中间那一步摊开：query → 命中分块 +
 * 相似度。对 BYOK 的本地工具尤其要紧：embedding 模型好不好、切分合不合适，
 * 都只能从这里看出来。
 *
 * 【命中卡片为什么不复用 CitationBadge】契约里两者字段是逐个对应的
 *（chunkId / documentId / filename / snippet / score），复用是原本的意图。
 * 但 CitationBadge 是**行内**引用角标：渲染成 `[1] [2] [3]` 的上标，
 * 内容藏在 HoverCard 里、一次只看得见一个。调试视图要的恰好相反——
 * 几个片段的相似度要能横向比较，得同时摊在屏幕上。想在触屏上展开更是
 * 没有等价交互（§15，那是 issue #86 的范围）。所以这里自己渲染一份
 * 片段的展示：同样的字段、不同的呈现目的。
 *
 * 【它是读操作】虽然契约里写的是 POST（query 在请求体里），它不改任何数据。
 * 所以失败**不进 toast**，留在页内的 Alert 里（§16：写操作 toast / 查询失败
 * 页内 Alert，判据是语义而不是方法）。
 */
export function KnowledgeSearchPanel({ kbId }: { kbId: string }) {
  const search = useKnowledgeSearch(kbId)
  const [query, setQuery] = useState('')
  const [topK, setTopK] = useState('')
  // 只有"查询是空的"这一条等到提交才提示——用户刚打开页面不该先挨一句红字。
  // topK 的越界提示是渲染期算出来的（下面的 topKError），因为它跟着输入实时变。
  const [submitError, setSubmitError] = useState<string | null>(null)

  const topKError = validateTopK(topK)
  const hits = search.data?.hits

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const q = query.trim()
    if (q === '') {
      setSubmitError('先输入要检索的内容')
      return
    }
    // topK 越界时按钮本来就是禁用的（见下面），这里再拦一道是因为回车也能提交
    // 表单——绕过禁用按钮。**不夹取**：把 80 改成 50 发出去，用户会以为
    // "只命中这么多"（契约里写明了这一点）。
    if (topKError) return
    setSubmitError(null)
    // topK 留空时传 undefined：JSON 序列化会丢掉这个键，服务端用默认值。
    search.mutate({ query: q, topK: topK.trim() === '' ? undefined : Number(topK) })
  }

  return (
    <div className="space-y-4">
      <form onSubmit={handleSubmit} className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1 space-y-1.5">
          {/* 标签必须真的和输入框关联：只有 placeholder 的话，
              读屏软件念的是一个没有名字的输入框。 */}
          <Label htmlFor="kb-search-query">查询</Label>
          <Input
            id="kb-search-query"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setSubmitError(null)
            }}
            placeholder="用一句自然语言问，例如：向量检索用的是什么索引？"
            aria-invalid={submitError ? true : undefined}
            aria-describedby={submitError ? 'kb-search-query-error' : undefined}
          />
        </div>
        <div className="w-full space-y-1.5 sm:w-28">
          <Label htmlFor="kb-search-topk">返回条数</Label>
          <Input
            id="kb-search-topk"
            value={topK}
            onChange={(e) => {
              setTopK(e.target.value)
              setSubmitError(null)
            }}
            inputMode="numeric"
            placeholder="默认 5"
            aria-invalid={topKError ? true : undefined}
            aria-describedby={topKError ? 'kb-search-topk-error' : undefined}
          />
        </div>
        <Button type="submit" disabled={search.isPending || topKError !== null}>
          <Search />
          {search.isPending ? '检索中…' : '检索'}
        </Button>
      </form>

      {/* 字段级的失败贴在字段旁边（§20）。topK 的提示挂在那一个输入框下，
          这样读屏软件顺着 aria-describedby 就能读到它。**只渲染一次**——
          提交时不再把它抄一份到 submitError，否则同一句话在页面上出现两遍。 */}
      {topKError && (
        <p id="kb-search-topk-error" className="text-destructive text-xs">
          {topKError}
        </p>
      )}
      {submitError && (
        <p id="kb-search-query-error" className="text-destructive text-xs">
          {submitError}
        </p>
      )}

      {search.isPending ? (
        <SearchingSkeleton />
      ) : search.error ? (
        // 【"检索失败"和"没有命中"是两件事】前者是一个 4xx/5xx（Key 不对、
        // 配额用完、知识库没了），后者是一个成功的空数组。合成一个文案，
        // 用户就分不清该改配置还是该改问法。
        <Alert variant="destructive">
          <AlertTitle>检索失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={search.error} />
          </AlertDescription>
        </Alert>
      ) : hits && hits.length === 0 ? (
        <div className="border-border rounded-xl border border-dashed px-4 py-10 text-center">
          <p className="font-medium">没有命中任何分块</p>
          <p className="text-muted-foreground mt-1 text-sm">
            检索本身是成功的，只是这个知识库里没有和它相关的内容。确认文档已经处理完
            （状态「就绪」、分块数不为 0），或者换个说法再试。
          </p>
        </div>
      ) : hits ? (
        <div className="space-y-2">
          <p className="text-muted-foreground text-xs">命中 {hits.length} 个分块，按相似度从高到低</p>
          {hits.map((hit) => (
            <SearchHitCard key={hit.chunkId} hit={hit} />
          ))}
        </div>
      ) : null}
    </div>
  )
}

/**
 * 一个命中分块。字段与 SSE 的 citation 事件逐个对应，所以读法与对话页
 * 的引用一致：文件名 + 相似度 + 片段。
 *
 * 【片段在这里不截断】引用卡片（CitationBadge）是行内角标，空间有限，
 * 用 line-clamp 收起来是对的；调试视图恰恰要看全文——截断的片段没法判断
 * "是切分切坏了，还是真的就这一段最相关"。
 */
function SearchHitCard({ hit }: { hit: Hit }) {
  return (
    <div className="border-border rounded-xl border p-4">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <FileText className="text-muted-foreground size-4 shrink-0" />
          <span className="truncate text-sm font-medium" title={hit.filename}>
            {hit.filename}
          </span>
        </div>
        <span className="text-muted-foreground shrink-0 text-xs">
          相似度 {(hit.score * 100).toFixed(0)}%
        </span>
      </div>
      <p className="text-muted-foreground mt-2 text-xs whitespace-pre-wrap">{hit.snippet}</p>
    </div>
  )
}

/** 检索中的骨架：留白会让用户以为"点了没反应"。 */
function SearchingSkeleton() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 3 }, (_, i) => (
        <Skeleton key={i} className="h-20 rounded-xl" />
      ))}
    </div>
  )
}
