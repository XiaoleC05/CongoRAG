import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'

/**
 * Provider 列表在缓存里的 key。同一份写法见 useKnowledgeBases.ts 的注释：
 * 读和写两处必须用同一个常量，拼错一处不会报错，只会让界面"点了没反应"。
 */
export const PROVIDERS_KEY = ['providers']

/**
 * 读：已配置的模型接入列表（不含明文 Key）。
 *
 * 引导页用它判断"要不要显示引导表单"——如果已经配置过至少一个 provider，
 * 主界面应该直接可用，不用每次开机都走一遍引导流程。
 */
export function useProviders() {
  return useQuery({
    queryKey: PROVIDERS_KEY,
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/providers')
      if (error) throw error
      return data
    },
  })
}

/**
 * 提交表单时打的那一个请求的完整入参。
 *
 * 【capabilities 四个字段都不带 `?`】契约里它们各自有 `default`，
 * openapi-typescript 因此把它们生成成非可选的 boolean——从"写请求"的
 * 角度看这是对的：既然要传这个对象，就该给出四个字段各自真实的值，
 * 不留给生成器猜。页面这边本来就是四个 Checkbox 各自控制一个状态，
 * 天然满足这个形状。
 */
export type CreateProviderInput = {
  baseUrl: string
  apiKey: string
  chatModel: {
    modelId: string
    capabilities: {
      chat: boolean
      streaming: boolean
      toolCalling: boolean
      reasoning: boolean
    }
    contextWindow: number
    maxOutputTokens: number
    tokenizerType: string
  }
  embeddingModelId: string
}

/**
 * 写：保存并开始（引导页的"保存并开始"按钮）。
 *
 * 【为什么不是乐观更新】其它模块的写操作（知识库改名/删除）在
 * KnowledgeBasesPage 里都能乐观更新，因为失败的代价很小（页面刷新一下）。
 * 这一次不一样：请求成功与否取决于一次真实的网络探测（后端探测 embedding
 * 维度），往返通常要几百毫秒到几秒，乐观更新会让用户以为"保存成功了"，
 * 结果几秒后又被回滚——比等待更让人困惑。
 */
export function useCreateProvider() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (input: CreateProviderInput) => {
      const { data, error } = await api.POST('/api/v1/providers', {
        body: input,
      })
      if (error) throw error
      return data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY }),
  })
}
