import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'

/**
 * 知识库列表在缓存里的 key。
 *
 * 读和写两处都用它，必须从同一个地方取——两处各写一个字面量的话，
 * 拼错一处不会报错，只会让界面"点了没反应"。
 */
export const KNOWLEDGE_BASES_KEY = ['knowledge-bases']

/**
 * 读：知识库列表。
 *
 * 页面组件不直接调 `api.*`，一律通过这个 hook。
 * 好处是请求细节（路径、错误转换、缓存 key）只在一个地方，
 * 页面只关心 `data / isPending / error` 三种状态。
 */
export function useKnowledgeBases() {
  return useQuery({
    queryKey: KNOWLEDGE_BASES_KEY,
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/knowledge-bases')
      // openapi-fetch 用返回值而非异常表示失败，转成 throw 交给 TanStack Query
      if (error) throw error
      return data
    },
  })
}

/**
 * 写：新建 / 改名 / 删除。
 *
 * 三个操作共用一个"收尾动作"：作废列表缓存。
 *
 * 不做的后果是——数据改成功了，但页面上的列表还是旧的，
 * 用户点了按钮什么变化都看不到，而且【不会报错】。
 * 作废之后 TanStack Query 会自动重新拉取。
 */
export function useKnowledgeBaseMutations() {
  const queryClient = useQueryClient()

  const invalidateList = () =>
    queryClient.invalidateQueries({ queryKey: KNOWLEDGE_BASES_KEY })

  const create = useMutation({
    mutationFn: async (name: string) => {
      const { error } = await api.POST('/api/v1/knowledge-bases', {
        body: { name },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  const rename = useMutation({
    mutationFn: async (vars: { id: string; name: string }) => {
      // 路径里的 {id} 是占位符，实际值走 params.path
      const { error } = await api.PATCH('/api/v1/knowledge-bases/{id}', {
        params: { path: { id: vars.id } },
        body: { name: vars.name },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/api/v1/knowledge-bases/{id}', {
        params: { path: { id } },
      })
      if (error) throw error
    },
    onSuccess: invalidateList,
  })

  return { create, rename, remove }
}
