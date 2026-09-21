// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { ReactNode } from 'react'
import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { PROVIDERS_KEY, useCreateProvider } from '@/hooks/useProviders'
import type { CreateProviderInput } from '@/hooks/useProviders'

// 只关心 mutation 成功后往缓存里写了什么，不测网络层——api 是契约生成的，
// 它的返回形状已经在契约里定死；换成可控的 mock 才能只喂 data。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))

type ProviderWithModels = Schemas['ProviderWithModels']

// openapi-fetch 的返回形状是 { data, error, response }，泛型跟着 schemaPath 走，
// 测试里只关心 data，所以把签名收敛成最松的 Mock 再用。
const postMock = api.POST as unknown as Mock

const savedProvider: ProviderWithModels = {
  id: '11111111-1111-4111-8111-111111111111',
  baseUrl: 'https://api.example.com/v1',
  createdAt: '2026-09-21T00:00:00Z',
  models: [],
}

const input: CreateProviderInput = {
  baseUrl: 'https://api.example.com/v1',
  apiKey: 'sk-test',
  chatModel: {
    modelId: 'gpt-4o-mini',
    capabilities: { chat: true, streaming: true, toolCalling: false, reasoning: false },
    contextWindow: 128000,
    maxOutputTokens: 4096,
    tokenizerType: 'cl100k_base',
  },
  embeddingModelId: 'text-embedding-3-small',
  // 契约里它带 default，openapi-typescript 因此把它生成成非可选的 boolean——
  // 调用方必须明确表态，不让"没传"和"传了 false"混在一起。
  allowEmbeddingReset: false,
}

describe('useCreateProvider', () => {
  afterEach(() => {
    postMock.mockReset()
  })

  // 回归：保存成功时用户还停在 /onboarding，RequireProvider 没挂载，
  // ['providers'] 这个 query 没有观察者。此时单靠 invalidateQueries
  // （默认 refetchType: 'active'）只会把它标记为 stale、不会重新拉取，
  // 缓存里始终没有数据。必须把返回的这条写穿缓存。
  it('保存成功后把新 provider 写进 providers 缓存', async () => {
    postMock.mockResolvedValue({ data: savedProvider, error: undefined })

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    )

    const { result } = renderHook(() => useCreateProvider(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync(input)
    })

    const cached = queryClient.getQueryData<ProviderWithModels[]>(PROVIDERS_KEY) ?? []
    expect(cached).toHaveLength(1)
    expect(cached[0]?.id).toBe(savedProvider.id)
  })
})
