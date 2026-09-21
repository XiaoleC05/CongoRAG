import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import { flattenPages, nextPageParam } from '@/lib/pagination'

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
