import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'

type ProviderWithModels = Schemas['ProviderWithModels']

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
    // 【为什么要写穿缓存，而不是只作废】保存成功时用户还停在 /onboarding
    // 上，RequireProvider 没有挂载，['providers'] 这个 query 是无观察者的。
    // 此时 invalidateQueries 默认 refetchType: 'active' 只会把它标记为
    // stale、不会重新拉取；等用户跳回主界面、RequireProvider 重新挂载时，
    // 它读到的仍是那份"空"的旧缓存，于是立刻把人弹回引导页——provider 明明
    // 已经创建成功。把返回的这条写进缓存，守卫就有真实数据可读，不依赖
    // "将来某个 observer 挂载时会不会补拉一次"。
    // 作废仍然保留：它让随后挂载的 observer 去后端核对一次（比如后端补齐了
    // models 列表），写穿只是保证这一刻的判定是对的。
    // 写穿发生在 onSuccess 里，也就是服务端已经确认创建之后——上面说的
    // "不做乐观更新"仍然成立，这里只是把已确认的结果放进缓存。
    onSuccess: (created) => {
      queryClient.setQueryData<ProviderWithModels[]>(PROVIDERS_KEY, (old) => [...(old ?? []), created])
      return queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY })
    },
  })
}
