import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'

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
  return useQuery({
    queryKey: documentsKey(kbId),
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/knowledge-bases/{id}/documents', {
        params: { path: { id: kbId } },
      })
      if (error) throw error
      return data
    },
    refetchInterval: (query) => {
      const docs = query.state.data
      if (!docs) return false
      const stillProcessing = docs.some(
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

  return { upload, remove }
}
