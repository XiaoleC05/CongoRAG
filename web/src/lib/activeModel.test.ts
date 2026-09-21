import { describe, expect, it } from 'vitest'

import { latestChatModel } from './activeModel'

type ProviderWithModels = Parameters<typeof latestChatModel>[0][number]
type ModelSummary = ProviderWithModels['models'][number]

function model(over: Partial<ModelSummary> & Pick<ModelSummary, 'modelId' | 'createdAt'>): ModelSummary {
  return {
    id: '00000000-0000-0000-0000-000000000000',
    kind: 'chat',
    capabilities: { chat: true, streaming: true, toolCalling: true, reasoning: false },
    contextWindow: 32000,
    maxOutputTokens: 4096,
    tokenizerType: 'cl100k_base',
    embeddingDim: 0,
    ...over,
  }
}

function provider(models: ModelSummary[]): ProviderWithModels {
  return { id: 'p1', baseUrl: 'https://example.test/v1', createdAt: '2026-01-01T00:00:00Z', models }
}

describe('latestChatModel', () => {
  it('取 createdAt 最新的那个 chat 模型', () => {
    const providers = [
      provider([
        model({ modelId: '旧模型', createdAt: '2026-01-01T00:00:00Z' }),
        model({ modelId: '新模型', createdAt: '2026-03-01T00:00:00Z' }),
      ]),
    ]

    expect(latestChatModel(providers)?.modelId).toBe('新模型')
  })

  it('忽略 embedding 行——它再新也不是 chat 模型', () => {
    const providers = [
      provider([
        model({ modelId: 'chat 模型', createdAt: '2026-01-01T00:00:00Z' }),
        model({ modelId: 'embedding 模型', createdAt: '2026-03-01T00:00:00Z', kind: 'embedding' }),
      ]),
    ]

    expect(latestChatModel(providers)?.modelId).toBe('chat 模型')
  })

  it('跨多个 provider 取全局最新的那一个', () => {
    const providers = [
      provider([model({ modelId: 'provider A', createdAt: '2026-01-01T00:00:00Z' })]),
      provider([model({ modelId: 'provider B', createdAt: '2026-02-01T00:00:00Z' })]),
    ]

    expect(latestChatModel(providers)?.modelId).toBe('provider B')
  })

  it('没有 chat 模型时返回 null', () => {
    expect(latestChatModel([])).toBeNull()
    expect(
      latestChatModel([provider([model({ modelId: 'e', createdAt: '2026-01-01T00:00:00Z', kind: 'embedding' })])]),
    ).toBeNull()
  })

  it('按时间戳比较，不受时区写法影响', () => {
    // 同一时刻，一个用 Z 一个用 +08:00 —— 字符串比较会把 +08:00 那个判成更晚，
    // 但换算成绝对时间它们相等，所以"更新的那个"应该是后出现但时间相等的这条
    // 之外的另一条：这里用真正更晚的 +08:00 表达来钉住换算是对的。
    const providers = [
      provider([
        model({ modelId: 'UTC', createdAt: '2026-01-01T00:00:00Z' }),
        model({ modelId: '东八区更晚', createdAt: '2026-01-01T09:00:00+08:00' }),
      ]),
    ]

    expect(latestChatModel(providers)?.modelId).toBe('东八区更晚')
  })
})
