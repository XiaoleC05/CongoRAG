import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { USAGE_KEY } from '@/hooks/useUsage'

type ProviderWithModels = Schemas['ProviderWithModels']
type ModelSummary = Schemas['ModelSummary']

/**
 * Provider 列表在缓存里的 key。同一份写法见 useKnowledgeBases.ts 的注释：
 * 读和写两处必须用同一个常量，拼错一处不会报错，只会让界面"点了没反应"。
 */
export const PROVIDERS_KEY = ['providers']

/**
 * 找出「当前生效的 embedding 模型」——`lib/activeModel.ts` 里
 * `latestChatModel` 的对称项，判据同 `internal/llm/model.go` 的
 * `LatestByKind`：同 kind 里 `createdAt` 最新的那一个。
 *
 * 【为什么它是单独一个函数，而不是把 latestChatModel 泛化一下】那个函数
 * 在 `lib/activeModel.ts`，而本批次只允许改 hooks/ 下这三个文件。搬家会
 * 同时动两个不在清单里的文件，所以先在这里放一份对称实现，并在注释里
 * 指向它——改动 `LatestByKind` 的判据时**两处都要跟**（不跟的后果是设置页
 * 显示一个过期的模型名和维度；不会报错，只是信息是假的）。
 *
 * 【为什么维度只看 embedding 模型】契约里 `ModelSummary.embeddingDim` 对
 * chat 模型恒为 0（注释写明），拿 chat 模型的 0 去显示"当前向量维度 0"
 * 是纯误导，所以这里只遍历 kind === 'embedding'。
 */
export function latestEmbeddingModel(
  providers: ProviderWithModels[],
): ModelSummary | null {
  let latest: ModelSummary | null = null

  for (const provider of providers) {
    for (const model of provider.models ?? []) {
      if (model.kind !== 'embedding') continue
      // 比时间戳而不是比字符串：createdAt 是带时区偏移的 RFC3339 串，
      // 字符串比较只在同一个偏移下碰巧成立（§11 是同一类坑）。
      if (
        latest === null ||
        new Date(model.createdAt).getTime() > new Date(latest.createdAt).getTime()
      ) {
        latest = model
      }
    }
  }

  return latest
}

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
  /**
   * 用户是否已经确认"换模型会清空已有向量并自动重建"（issue #39）。
   *
   * 【为什么不是可选的】契约里它有 default，openapi-typescript 因此把它
   * 生成成非可选的 boolean——从"写请求"的角度这是对的：调用方必须明确
   * 表态，不让"没传"和"传了 false"混在一起。第一次提交传 false；
   * 服务端返回 embedding_change_requires_reindex 时，用户确认后传 true 重发。
   */
  allowEmbeddingReset: boolean
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

/**
 * 改一个模型条目时要提交的东西。
 *
 * 【为什么分成 `id` 和 `body` 两层，而不是拉平成一个对象】契约里
 * `UpdateModelRequest.modelId` 是**模型名**（`gpt-4o-mini` 那种），而路径里的
 * `{id}` 是 `llm_models` 的主键。两者都叫 "id"，拉平成一个对象就必须重名，
 * 类型也拦不住写错。分开之后 `id` 永远是主键、`body` 永远是那份整体替换的
 * 请求体，调用点不可能搞混。
 */
export type UpdateModelInput = {
  /** llm_models 的主键，走路径参数 */
  id: string
  body: Schemas['UpdateModelRequest']
}

/**
 * 写：改 / 删一个模型条目（issue #83 的"编辑 / 删除模型"）。
 *
 * 【为什么在 models/{id} 上，而不是 providers/{id}】契约里 provider 本身
 * 只有 get / post——**没有 PATCH、没有 DELETE**。能改能删的粒度是"模型条目"。
 * 编辑/删除 provider 这一层（换 Base URL、换 Key、整个删掉）没有端点，
 * 所以设置页上也不给这两个入口：界面上的每个按钮背后都必须有一个真实
 * 存在的端点，做一个点了没反应的按钮，用户只会以为是自己点错了。
 *
 * 【两个操作都作废整个 provider 列表】`PROVIDERS_KEY` 是前缀失效，
 * 模型是 provider 的子结构，列表是唯一的读点，没有单条缓存要单独处理。
 */
export function useModelMutations() {
  const queryClient = useQueryClient()

  const update = useMutation({
    mutationFn: async (input: UpdateModelInput) => {
      const { data, error } = await api.PATCH('/api/v1/models/{id}', {
        params: { path: { id: input.id } },
        body: input.body,
      })
      if (error) throw error
      return data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY }),
  })

  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/api/v1/models/{id}', {
        params: { path: { id } },
      })
      if (error) throw error
    },
    onSuccess: () => {
      // 【为什么这里要多作废一次用量缓存】`token_usage.model_id` 的外键是
      // ON DELETE CASCADE（migrations/0003），删掉一个模型会把它历史上的
      // 用量行一起删掉。不作废的话，用量页上的数字还是删之前那一份——
      // 数据已经变了、页面没刷新，而且不报错（§3 说的正是这种坏法）。
      void queryClient.invalidateQueries({ queryKey: USAGE_KEY })
      return queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY })
    },
  })

  return { update, remove }
}

