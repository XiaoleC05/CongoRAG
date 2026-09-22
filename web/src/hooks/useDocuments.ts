import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { flattenPages, nextPageParam } from '@/lib/pagination'

/** 文档状态。直接用契约里的枚举，前端不另立一套（web/README.md §5）。 */
export type DocumentStatus = Schemas['Document']['status']

/** 文档列表的排序方式（issue #92）。 */
export type DocumentSort = 'newest' | 'oldest' | 'status'

/** 文档列表的筛选方式（issue #92）。'all' = 不筛。 */
export type DocumentFilter = 'all' | DocumentStatus

/**
 * 筛选 / 排序的文案与取值顺序。
 *
 * 【为什么放在 hooks 而不是组件里】同一句话要在两处出现：工具条上的开关，
 * 以及"筛完没有结果"那个空态的说明。散着写迟早有一处对不上，而且对不上
 * 不会报错——只是同一件事在两个地方叫不同的名字。
 *
 * 【两张 Record 表把"漏了"变成编译错误】状态枚举将来多一个值时，
 * `Record<DocumentFilter, string>` 会直接报缺项——漏的是文案，不是行为，
 * 所以只有类型能拦住它。
 *
 * 【顺序用数组显式写出来】对象的键顺序是隐式的，改一次不会有人发现。
 * 这两张数组表和上面两张 Record 表都要手动同步：数组声明成 `DocumentFilter[]`
 * 只是"元素合法"，多一个状态时不会自动变红，加状态的人记得回来加一行。
 */
export const FILTER_LABEL: Record<DocumentFilter, string> = {
  all: '全部',
  queued: '排队中',
  processing: '处理中',
  ready: '就绪',
  failed: '失败',
}

export const SORT_LABEL: Record<DocumentSort, string> = {
  newest: '最新在前',
  oldest: '最早在前',
  status: '按状态',
}

export const DOCUMENT_FILTERS: DocumentFilter[] = [
  'all',
  'queued',
  'processing',
  'ready',
  'failed',
]

export const DOCUMENT_SORTS: DocumentSort[] = ['newest', 'oldest', 'status']

/**
 * 一个知识库下的文档列表在缓存里的 key。
 *
 * 按知识库 id 分开缓存——切换到另一个知识库的详情页不该看到上一个的
 * 列表残留，也不该因为共用一个 key 而互相误触发失效。
 */
export const documentsKey = (kbId: string) => ['documents', kbId]

/**
 * 读：一个知识库下的文档列表。
 *
 * 【轮询】文档上传后是异步处理的（queued → processing → ready/failed），
 * 列表页需要看到状态变化,不能只在打开页面那一刻拉一次就不再更新。
 * `refetchInterval` 用函数形式：只要列表里还有非终态（queued/processing）
 * 的文档就继续轮询，全部到达终态（ready/failed）后自动停止——
 * 不用一个单独的 useEffect 去启停定时器，TanStack Query 自己管这个生命周期。
 */
export function useDocuments(kbId: string) {
  return useInfiniteQuery({
    queryKey: documentsKey(kbId),
    // null 表示"从头开始"（第一页）。用 null 而不是空串是因为它和
    // nextCursor 的 nullable 语义一致，getNextPageParam 里不必再转换。
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET('/api/v1/knowledge-bases/{id}/documents', {
        params: { path: { id: kbId }, query: { cursor: pageParam ?? undefined } },
      })
      if (error) throw error
      return data
    },
    getNextPageParam: nextPageParam,
    // 【轮询在分页之后仍然保留】判据不变（列表里还有非终态的文档就继续轮询），
    // 只是要在摊平之后的集合上判。
    //
    // 【代价要如实记住】v5 的 useInfiniteQuery 没有"只重取第一页"的开关
    // （refetch 会把已加载的每一页都重取一遍），所以用户点过 N 次「加载更多」
    // 之后，每次轮询就是 N × 50 行。方向仍然是对的——用户没加载过更多时
    // 成本不变，而加载过的历史是他自己要看的。
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return false
      const stillProcessing = flattenPages(data).some(
        (d) => d.status === 'queued' || d.status === 'processing',
      )
      return stillProcessing ? 2000 : false
    },
  })
}

/**
 * 写：上传 / 删除。
 *
 * 上传成功只是"排上了队"（202 + status: queued），不代表处理完成——
 * 页面看到的"处理中"状态由上面的轮询驱动，不是这个 mutation 的职责。
 */
export function useDocumentMutations(kbId: string) {
  const queryClient = useQueryClient()

  const invalidateList = () =>
    queryClient.invalidateQueries({ queryKey: documentsKey(kbId) })

  const upload = useMutation({
    mutationFn: async (file: File) => {
      const { error } = await api.POST('/api/v1/knowledge-bases/{id}/documents', {
        params: { path: { id: kbId } },
        // 【为什么这里要断言类型】OpenAPI 的 `format: binary` 被
        // openapi-typescript 翻成 TS 的 `string`（规范里没有更贴切的类型），
        // 所以生成的请求体类型是 `{ file: string }`。但运行时真正要传的
        // 是浏览器的 File 对象，靠下面的 bodySerializer 转成 FormData——
        // 断言只是让 TS 允许这次调用，实际发出去的从来不是一个字符串。
        body: { file } as never,
        bodySerializer(body) {
          const form = new FormData()
          // 双重断言（先到 unknown 再到目标类型）：TS 看到的类型是
          // { file: string }（见上面 body 那一行的注释），和真实的
          // { file: File } 没有足够的重叠，直接断言会被拒绝。
          form.append('file', (body as unknown as { file: File }).file)
          // 【不手动设置 Content-Type】必须让浏览器自己生成这个头——
          // multipart 请求的 Content-Type 里带一个随机 boundary
          //（形如 multipart/form-data; boundary=----xxx），手写的话
          // boundary 和请求体实际的分隔符对不上，服务端解析不出字段。
          return form
        },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  const remove = useMutation({
    mutationFn: async (documentId: string) => {
      const { error } = await api.DELETE('/api/v1/documents/{id}', {
        params: { path: { id: documentId } },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  /**
   * 重新索引一份文档（issue #39）。
   *
   * 用途有两个：换 embedding 模型之后重跑，以及某一次处理失败/超时之后
   * 只重跑那一份。服务端返回 202，状态回到 queued——上面的轮询会自动
   * 接着看它跑到终态。
   *
   * 【它返回 409 是正常的】文档已经在排队或正在处理时会 409（重复排没有
   * 意义）。调用方按 conflict 呈现即可，不要当成故障。
   */
  const reindex = useMutation({
    mutationFn: async (documentId: string) => {
      const { error } = await api.POST('/api/v1/documents/{id}/reindex', {
        params: { path: { id: documentId } },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  return { upload, remove, reindex }
}

/**
 * 写：整库重新索引（issue #39）。
 *
 * 【为什么单独一个 hook 而不是塞进 useDocumentMutations】它不是文档级的
 * 操作，作用对象是这个知识库；放在一起会让那个 hook 的语义变成"一堆和
 * 文档有关但粒度不同的写操作"。两者共用同一个列表 key，所以失效逻辑一致。
 */
export function useReindexKnowledgeBase(kbId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/api/v1/knowledge-bases/{id}/reindex', {
        params: { path: { id: kbId } },
      })
      if (error) throw error
      return data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: documentsKey(kbId) }),
  })
}

/**
 * 状态排序用的优先级。
 *
 * 【顺序照抄状态机，不是自己发明的】`internal/knowledge/document.go` 的
 * 四态是 queued → processing → ready → failed，这里保持同一个方向。
 * 前两个是"还在动"的，用户最常问的"哪几份还在处理"因此落在列表最上面；
 * failed 在最后——想专门找它用筛选，一次点击就到（排序不负责这件事）。
 *
 * 值本身没有含义，只用来比较——不要把它当成状态码存进任何地方。
 */
const STATUS_RANK: Record<DocumentStatus, number> = {
  queued: 0,
  processing: 1,
  ready: 2,
  failed: 3,
}

/** 上传时间戳。解析不了时给 0，让坏数据沉到最旧那一端而不是把排序整个搞乱。 */
const createdAt = (doc: Schemas['Document']) => {
  const t = new Date(doc.createdAt).getTime()
  return Number.isNaN(t) ? 0 : t
}

/**
 * 文档列表的排序 + 筛选（issue #92）。
 *
 * 【为什么是客户端做】现有端点的查询参数只有 `limit` / `cursor`，没有排序
 * 与筛选参数。加参数要先动 `contracts/openapi.yaml`（本批次不动），所以这里是
 * 纯客户端的视图变换——发出去的请求一个字节都没变。
 *
 * 【数据规模上限的假设】本地单机工具里，一个知识库是几十份文档的量级，
 * 每页 50 条，在内存里排一遍的代价可以忽略。**这个前提不成立时方案就废了**：
 * 没翻过的页不参与排序与筛选，用户会把"筛不出来"当成"本来就没有"。
 * 单个知识库真到几千份文档时，必须把 sort / status 挪成服务端参数。
 *
 * 【它和游标的关系（这条是 issue #92 点名要写清的）】游标编码的是
 * "服务器顺序（上传时间倒序）里的位置"。排序与筛选不改请求参数，所以游标
 * 在排序前后指的是同一个东西，**不存在"拿着旧排序的位置去取新排序的数据"
 * 这种串页**，也不需要重置。**将来若把排序挪到服务端，排序变更时必须把游标
 * 清回 null**——那一刻游标才开始编码排序结果里的位置，不清就是串页。
 *
 * 【不改变入参数组】sort 是原地操作，摊平出来的数组虽然是新的，也不去赌
 * 调用方将来不会传缓存里的数组——复制一次的成本在这里没有意义。
 */
export function sortAndFilterDocuments(
  docs: Schemas['Document'][],
  sort: DocumentSort,
  filter: DocumentFilter,
): Schemas['Document'][] {
  const filtered = filter === 'all' ? docs : docs.filter((doc) => doc.status === filter)

  // 默认顺序就是服务器给的顺序（上传时间倒序），不必在客户端再排一遍。
  if (sort === 'newest') return filtered

  if (sort === 'oldest') {
    // 【按时间戳比，不按字符串比】契约里的时间是带时区偏移的 ISO 串
    // （`2026-09-21T00:00:05+08:00`），字符串比较在不同偏移之间会得出错误顺序，
    // 而且不会报错——和 web/README.md §11 不切片日期是同一类坑。
    return [...filtered].sort((a, b) => createdAt(a) - createdAt(b))
  }

  // 同状态内按上传时间倒序：分组边界看得出来，组内仍然是"最近的在上面"。
  return [...filtered].sort(
    (a, b) => STATUS_RANK[a.status] - STATUS_RANK[b.status] || createdAt(b) - createdAt(a),
  )
}

/**
 * 读：在指定知识库里做一次向量检索（issue #77 的检索调试视图）。
 *
 * 【为什么是 useMutation 而不是 useQuery】契约里它写成 POST（query 在请求体里），
 * 每次调用都是用户主动发起的一次探查，没有"同一个 key 的缓存值得复用"这回事——
 * 用 useQuery 反而要自己编一个 queryKey，还要面对窗口聚焦时自动重跑的问题。
 * 调试视图要的是"我点了才查"，mutation 的语义对得上。
 *
 * 【它是读语义，不是写语义】虽然走 POST，它不改任何数据。所以失败**不进 toast**，
 * 由调用方在页内渲染 Alert——web/README.md §16 分的是"写操作 toast / 查询失败
 * 页内 Alert"，判据是语义，不是 HTTP 方法。
 *
 * 【topK 越界不在这里夹取】契约是 minimum 1 / maximum 50，超了后端返 400。
 * 夹取会让用户以为"只命中这么多"，而实际是被截断了——调试视图上这个区别
 * 很关键（契约里 KnowledgeSearchRequest 的注释写明了这一点）。
 * 调用方的客户端校验只是省一次白跑的往返（§6），真正的强制在后端。
 */
export function useKnowledgeSearch(kbId: string) {
  return useMutation({
    mutationFn: async (input: { query: string; topK?: number }) => {
      const { data, error } = await api.POST('/api/v1/knowledge-bases/{id}/search', {
        params: { path: { id: kbId } },
        body: { query: input.query, topK: input.topK },
      })
      if (error) throw error
      return data
    },
  })
}
