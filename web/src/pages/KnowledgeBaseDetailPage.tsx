import { ArrowLeft, FileText, MessageSquarePlus, RotateCw, Upload } from 'lucide-react'
import { useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'

import type { Schemas } from '@congorag/api-client'

import { ErrorText } from '@/components/ErrorText'
import { DeleteDocumentDialog } from '@/components/knowledge/DeleteDocumentDialog'
import { DocumentListToolbar } from '@/components/knowledge/DocumentListToolbar'
import { DocumentRowActions } from '@/components/knowledge/DocumentRowActions'
import { DocumentStatusBadge } from '@/components/knowledge/DocumentStatusBadge'
import { KnowledgeSearchPanel } from '@/components/knowledge/KnowledgeSearchPanel'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useCreateConversation } from '@/hooks/useConversations'
import {
  FILTER_LABEL,
  sortAndFilterDocuments,
  useDocumentMutations,
  useDocuments,
  useReindexKnowledgeBase,
} from '@/hooks/useDocuments'
import type { DocumentFilter, DocumentSort } from '@/hooks/useDocuments'
import { useErrorToast } from '@/hooks/useErrorToast'
import { useKnowledgeBases } from '@/hooks/useKnowledgeBases'
import { errorPresentation } from '@/lib/errors'
import { formatByteSize, formatDateTime } from '@/lib/format'
import { flattenPages } from '@/lib/pagination'

type Document_ = Schemas['Document']

/**
 * 写操作失败在页面上还剩多少要显示。
 *
 * 【为什么要过滤】这个页面的三个写操作（上传/开始对话/删除）失败后，
 * 能 toast 的那类（internal_error 之类，重试就行）已经由 useErrorToast 说过了，
 * 这里再渲染一份就是同一个错误两个 role="alert"，读屏软件念两遍。
 * 剩下的（要用户换一个文件、或知识库已经没了）toast 说不清，必须留在页面上——
 * 直接删掉 ErrorText 的话它们会凭空消失，那比重复更糟。
 */
const residual = (err: unknown) =>
  err && errorPresentation(err, 'mutation') !== 'toast' ? err : undefined

/**
 * 知识库详情页——文件列表 + 上传按钮 + 检索调试。
 *
 * 【方案参照：Dify 文档表格】文件名 + 状态 Badge + 分块数 + 上传按钮，
 * 前端引用方案 §2.4 点名的形态。
 *
 * 【三态 + 两种空态，一个不能少】isPending/error 是所有页面的硬规矩
 *（web/README.md §2），空态在这个页面尤其重要——新建的知识库打开详情页
 * 第一眼看到的就是空态，得清楚地引导去点上传。**"集合本来就是空的"和
 * "筛完没有结果"是两个不同的态**（§13/§19），文案与出口都不一样，
 * 所以下面把它们分成两个组件，不是同一个组件换句话。
 */
export default function KnowledgeBaseDetailPage() {
  const { id } = useParams<{ id: string }>()
  const kbId = id ?? ''

  // 知识库本身也可能还没加载完（比如直接用链接打开这个 URL），
  // 用它的名字做面包屑；找不到时退化成"知识库"这个通用文案，
  // 不阻塞文档列表本身的加载——两者是独立的数据源。
  const { data: kbs } = useKnowledgeBases()
  const kbName = kbs?.find((kb) => kb.id === kbId)?.name

  const {
    data: docPages,
    isPending,
    error,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useDocuments(kbId)
  const docs = flattenPages(docPages)
  const { upload, remove, reindex } = useDocumentMutations(kbId)
  const reindexAll = useReindexKnowledgeBase(kbId)
  const createConversation = useCreateConversation()
  const navigate = useNavigate()
  // 写操作的失败出口。三个 mutation 共用同一个——判据在 errorPresentation 里，
  // 不在这里按操作名各写各的。
  const showError = useErrorToast()

  const fileInputRef = useRef<HTMLInputElement>(null)

  // 排序 / 筛选是**视图状态**，不是服务器状态（issue #92）。
  // 它不进 query key：改了排序不该触发重新请求，理由见 useDocuments.ts 的
  // sortAndFilterDocuments（游标编码的是服务器顺序，和这里的排序无关）。
  const [sort, setSort] = useState<DocumentSort>('newest')
  const [filter, setFilter] = useState<DocumentFilter>('all')

  // 待删除的目标。null = 弹窗关闭。和知识库列表页同一个做法：
  // 弹窗状态放在页面这一层，行组件只负责"请求打开"。
  const [deleting, setDeleting] = useState<Document_ | null>(null)

  const visible = sortAndFilterDocuments(docs, sort, filter)

  // 【为什么"开始对话"在这里,而不是一个独立的"新建会话"页面】
  // 这一轮的会话天生要关联一个知识库才谈得上 RAG——见
  // internal/conversation/model.go 的注释:方案没有定义这个关联,
  // 是实现时补上的决策。知识库详情页因此是唯一自然的入口:
  // 用户已经在看"这个知识库有哪些文档",顺理成章地问"关于这些文档"。
  const handleStartConversation = () => {
    createConversation.mutate(
      { title: kbName ?? '新对话', knowledgeBaseId: kbId },
      {
        onSuccess: (conv) => navigate(`/conversations/${conv.id}`),
        onError: showError,
      },
    )
  }

  const handleReindex = (doc: Document_) => reindex.mutate(doc.id, { onError: showError })

  // 打开删除确认框前先清掉上一次的错误，否则重开时会看到已经过期的报错（§7）。
  const openDelete = (doc: Document_) => {
    remove.reset()
    setDeleting(doc)
  }

  const handleFileSelected = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    // 选完立刻清空 input 的 value：不清的话，连续两次选同一个文件时
    // onChange 不会再触发（浏览器认为"值没变"），第二次上传就悄悄失效。
    e.target.value = ''
    if (file) {
      upload.mutate(file, { onError: showError })
    }
  }

  return (
    <div className="mx-auto max-w-4xl p-6">
      <div className="mb-4">
        <Button variant="ghost" size="sm" asChild className="-ml-2">
          <Link to="/knowledge-bases">
            <ArrowLeft />
            返回知识库列表
          </Link>
        </Button>
      </div>

      {/* 【标题与按钮分两行，不是把桌面那一行压扁（issue #86）】三个按钮
          （开始对话 / 重新索引全部 / 上传文档）加上标题要 ~816px 才排得下，
          挤在一行的结果是最后一个按钮被推出屏幕外（实测 390px 下
          scrollWidth 492 > 390）。

          【为什么阈值是 lg（1024）而不是 sm（640）】实测出来的：768px 上侧栏
          展开占 256px、页面 p-6 再吃掉 48px，正文只剩 512px，这一行要 816px
          ——所以 640 就横排是错的，得等到 lg。flex-wrap 再兜住标题特别长的
          情况（知识库名字很长时按钮会自己换行，而不是把按钮挤没）。 */}
      <header className="mb-6 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <h1 className="text-xl font-semibold">{kbName ?? '知识库'}</h1>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            onClick={handleStartConversation}
            disabled={createConversation.isPending}
          >
            <MessageSquarePlus />
            {createConversation.isPending ? '创建中…' : '开始对话'}
          </Button>
          <Button
            variant="outline"
            onClick={() => reindexAll.mutate(undefined, { onError: showError })}
            disabled={reindexAll.isPending || (docs?.length ?? 0) === 0}
          >
            <RotateCw />
            {reindexAll.isPending ? '排队中…' : '重新索引全部'}
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            className="hidden"
            accept=".md,.txt,.markdown"
            onChange={handleFileSelected}
          />
          <Button onClick={() => fileInputRef.current?.click()} disabled={upload.isPending}>
            <Upload />
            {upload.isPending ? '上传中…' : '上传文档'}
          </Button>
        </div>
      </header>

      <ErrorText error={residual(upload.error)} className="mb-4" />
      <ErrorText error={residual(createConversation.error)} className="mb-4" />

      {isPending ? (
        <SkeletonList />
      ) : error ? (
        <Alert variant="destructive">
          <AlertTitle>加载失败</AlertTitle>
          <AlertDescription>
            <ErrorText error={error} />
          </AlertDescription>
        </Alert>
      ) : docs.length === 0 ? (
        <EmptyState onUpload={() => fileInputRef.current?.click()} />
      ) : (
        <>
          <DocumentListToolbar
            sort={sort}
            filter={filter}
            onSortChange={setSort}
            onFilterChange={setFilter}
            loadedCount={docs.length}
          />
          <div className="border-border overflow-hidden rounded-xl border">
            {visible.length === 0 ? (
              <NoMatchState
                filter={filter}
                loadedCount={docs.length}
                hasMore={!!hasNextPage}
                onClear={() => setFilter('all')}
              />
            ) : (
              <>
                {/* 【窄屏降级：表格转卡片（issue #86 / §15、§19）】
                    六列（文件名/状态/分块/大小/上传时间/操作）的 min-content
                    宽度是 510px：390px 上它被外层 `overflow-hidden` 裁掉——
                    "大小""上传时间""操作"三列用户根本看不见，而且因为裁掉了，
                    documentElement 的 scrollWidth 也看不出问题（只有表格自己
                    知道自己被切了）。

                    【阈值为什么是 lg（1024）】510px 是表格自己的宽度，还要加上
                    侧栏（768px 以上展开时占 256px）和页面 p-6 的 48px：要
                    ~814px 的视口才排得下。所以 640（sm）就换成表格是错的，
                    md（768）也不够——768 上实测仍溢出 48px。1024 起正文有
                    720px，才真的放得下。

                    【为什么是成对渲染而不是 JS 判断断点】用 matchMedia 在
                    JS 里选分支的话，首帧必然是"初始状态的那一个"，水合或
                    effect 跑完才换——窄屏上会先闪一下六列表格。两套 DOM 都
                    渲染、由 CSS 决定谁出现，就没有这一帧。

                    【重复的部分抽到下面的行内小件里】两个分支只有外壳不同
                    （一个 <td>、一个卡片里的一行），格子里装的东西是同一批。
                    整段复制两份的话，改一处忘一处的那一半会安静地长歪——
                    两个分支永远不会同时出现在屏幕上，看不出来。 */}
                <ul className="divide-border divide-y lg:hidden">
                  {visible.map((doc) => (
                    <li key={doc.id} className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <div className="flex min-w-0 flex-1 items-center gap-2">
                          <FilenameCell doc={doc} />
                        </div>
                        <DocumentRowActions
                          doc={doc}
                          onReindex={handleReindex}
                          onDelete={openDelete}
                          pending={reindex.isPending || remove.isPending}
                        />
                      </div>
                      {/* 【卡片里必须给数字补上名字】表格有表头，卡片没有：
                          只写一个 "1" 和一串 "1.0 KB"，用户看不出哪个是分块数。
                          顺序与表格的列一致，来回切换时不用重新找。 */}
                      <div className="text-muted-foreground mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                        <StatusCell
                          doc={doc}
                          onReindex={handleReindex}
                          reindexPending={reindex.isPending}
                        />
                        <span>分块 {doc.chunkCount}</span>
                        <span>{formatByteSize(doc.byteSize)}</span>
                        <span>{formatDateTime(doc.createdAt)}</span>
                      </div>
                    </li>
                  ))}
                </ul>

                <table className="hidden w-full text-sm lg:table">
                  <thead className="bg-muted/50 text-muted-foreground text-left">
                    <tr>
                      <th className="px-4 py-2 font-medium">文件名</th>
                      <th className="px-4 py-2 font-medium">状态</th>
                      <th className="px-4 py-2 font-medium">分块</th>
                      <th className="px-4 py-2 font-medium">大小</th>
                      <th className="px-4 py-2 font-medium">上传时间</th>
                      <th className="px-4 py-2">
                        {/* 操作列的表头留空，但读屏软件需要知道这一列是什么。
                            空表头 + 行内可访问名（"xxx 的操作"）已经够了，
                            所以这里放 sr-only 而不是可见文字。 */}
                        <span className="sr-only">操作</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((doc) => (
                      <tr key={doc.id} className="border-border border-t">
                        <td className="flex items-center gap-2 px-4 py-2">
                          <FilenameCell doc={doc} />
                        </td>
                        <td className="px-4 py-2">
                          <div className="flex items-center gap-2">
                            <StatusCell
                              doc={doc}
                              onReindex={handleReindex}
                              reindexPending={reindex.isPending}
                            />
                          </div>
                        </td>
                        {/* 【分块数为什么直接显示数字】契约里它是整数、不是可空：
                            没处理完就是 0。给 0 加个"—"之类的特判会让"确实切出
                            0 块"（切分策略有问题）和"还没轮到"看起来一样——
                            恰恰把 issue #82 要暴露的那个信号盖掉了。 */}
                        <td className="text-muted-foreground px-4 py-2">{doc.chunkCount}</td>
                        <td className="text-muted-foreground px-4 py-2">
                          {formatByteSize(doc.byteSize)}
                        </td>
                        <td className="text-muted-foreground px-4 py-2">
                          {formatDateTime(doc.createdAt)}
                        </td>
                        <td className="px-4 py-2 text-right">
                          <DocumentRowActions
                            doc={doc}
                            onReindex={handleReindex}
                            onDelete={openDelete}
                            pending={reindex.isPending || remove.isPending}
                          />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}
            {/* 【加载更多放在表格下方】列表按 created_at 倒序，更旧的在下面。
                hasNextPage 为假时整个不渲染——禁用会让用户以为等一下就有了。
                【筛完没结果时它也要在】没加载到的页不参与客户端的筛选，
                所以要留一条"继续往后翻"的路，否则用户会以为知识库里
                真的没有这类文档。 */}
            {hasNextPage && (
              <div className="border-border flex justify-center border-t py-2">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={isFetchingNextPage}
                  onClick={() => void fetchNextPage()}
                >
                  {isFetchingNextPage ? '加载中…' : '加载更多'}
                </Button>
              </div>
            )}
          </div>
        </>
      )}

      {/* 检索调试视图：RAG 项目里唯一能直接看见检索质量的地方（issue #77）。
          放在文档列表之后——它是工具，不是这一页的主体。 */}
      <section className="mt-8">
        <h2 className="mb-3 text-base font-semibold">检索调试</h2>
        <p className="text-muted-foreground mb-4 text-sm">
          用一句话查一次，看知识库到底召回了哪些分块、相似度多少。换了 embedding
          模型、调了切分策略，或者某次问答答得不对时，都可以在这里先确认"检索这一层"有没有问题。
        </p>
        <KnowledgeSearchPanel kbId={kbId} />
      </section>

      <DeleteDocumentDialog
        target={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        pending={remove.isPending}
        // 删除失败的两个去处和知识库列表页一致：能 toast 的由 useErrorToast 说，
        // 要用户看清楚的留在弹窗里（弹窗还开着，错误就贴在这儿）。
        error={residual(remove.error)}
        onConfirm={() => {
          if (!deleting) return
          remove.mutate(deleting.id, {
            onSuccess: () => setDeleting(null),
            onError: showError,
          })
        }}
      />
    </div>
  )
}

/**
 * 表格与卡片共用的两块内容（issue #86）。
 *
 * 【为什么抽到这一层】文档列表在窄屏是卡片、`lg:` 以上是表格，两套外壳
 * 里的内容必须是同一份。这两块是"格子里装的东西"，外壳（`<td>` 还是卡片
 * 里的一行）留给调用方——表格需要的是 `<td>`，卡片需要的是能换行的一行，
 * 让函数自己决定外壳反而两边都不合适。
 */
function FilenameCell({ doc }: { doc: Document_ }) {
  return (
    <>
      <FileText className="text-muted-foreground size-4 shrink-0" />
      <span className="truncate" title={doc.filename}>
        {doc.filename}
      </span>
    </>
  )
}

/**
 * 状态徽标 + 失败行的"重新索引"出路（issue #82 的"可操作的下一步"）。
 *
 * 【它指向重新索引，但**不判断**"这个状态能不能重试"】只是把行尾菜单里的
 * 同一个操作搬到用户正看着的地方；真不允许时后端返 409，按 conflict 呈现。
 * 前端的职责是别让用户猜。
 */
function StatusCell({
  doc,
  onReindex,
  reindexPending,
}: {
  doc: Document_
  onReindex: (doc: Document_) => void
  reindexPending: boolean
}) {
  return (
    <>
      <DocumentStatusBadge status={doc.status} />
      {doc.status === 'failed' && (
        <Button
          variant="link"
          size="sm"
          className="h-auto p-0 text-xs"
          aria-label={`重新索引 ${doc.filename}`}
          disabled={reindexPending}
          onClick={() => onReindex(doc)}
        >
          重新索引
        </Button>
      )}
    </>
  )
}

function SkeletonList() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 4 }, (_, i) => (
        <Skeleton key={i} className="h-12 rounded-lg" />
      ))}
    </div>
  )
}

/** 集合本来就是空的：知识库里一份文档都没有。出口是"上传"。 */
function EmptyState({ onUpload }: { onUpload: () => void }) {
  return (
    <div className="border-border flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
      <FileText className="text-muted-foreground mb-3 size-8" />
      <p className="font-medium">还没有文档</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        上传 Markdown 或纯文本文件，处理完就能检索问答。
      </p>
      <Button onClick={onUpload}>
        <Upload />
        上传第一个文档
      </Button>
    </div>
  )
}

/**
 * 筛完没有结果。**和上面的空态是两件事**（§13/§19）：这里知识库里有文档，
 * 只是当前条件下一条都不匹配。所以文案要说清"筛的是什么、在多少份里筛的"，
 * 出口是"清除筛选"而不是"去上传"。
 *
 * 【hasMore 的那句话不能省】筛选只作用于已经加载到客户端的页，更早的文档
 * 还没参与筛选。不说的话，用户会以为"这个知识库里就没有失败的文档"。
 */
function NoMatchState({
  filter,
  loadedCount,
  hasMore,
  onClear,
}: {
  filter: DocumentFilter
  loadedCount: number
  hasMore: boolean
  onClear: () => void
}) {
  return (
    <div className="flex flex-col items-center justify-center px-4 py-16 text-center">
      <p className="font-medium">没有「{FILTER_LABEL[filter]}」的文档</p>
      <p className="text-muted-foreground mt-1 mb-4 text-sm">
        已加载的 {loadedCount} 份里没有符合这个条件的。
        {hasMore && '更早的文档还没加载，可以点下面的「加载更多」继续找。'}
      </p>
      <Button variant="outline" onClick={onClear}>
        清除筛选
      </Button>
    </div>
  )
}
