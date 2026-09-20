// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { PROVIDERS_KEY } from '@/hooks/useProviders'
import { RequireProvider } from '@/router'

// 只关心 hooks 怎么用缓存做判定，不测网络层——api 是契约生成的，
// 它的返回形状已经在契约里定死；这里换成可控的 mock 才能精确摆出
// "缓存是旧的、重取还在飞"这一刻。
vi.mock('@congorag/api-client', () => ({ api: { GET: vi.fn(), POST: vi.fn() } }))

type ProviderWithModels = Schemas['ProviderWithModels']

// openapi-fetch 的返回形状是 { data, error, response }，泛型跟着 schemaPath 走，
// 测试里只关心 data，所以把签名收敛成最松的 Mock 再用。
const getMock = api.GET as unknown as Mock

/** 刚保存成功的那一条 provider（守卫只数条数，models 留空不影响）。 */
const savedProvider: ProviderWithModels = {
  id: '11111111-1111-4111-8111-111111111111',
  baseUrl: 'https://api.example.com/v1',
  createdAt: '2026-09-21T00:00:00Z',
  models: [],
}

/**
 * 只挂 RequireProvider 这一层，不挂真实的 AppLayout 和页面——
 * 要验证的是守卫自己怎么用 providers 缓存做判定。
 * /onboarding 挂在守卫外面，和 router.tsx 里的结构一致。
 */
function renderGuard(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/knowledge-bases']}>
        <Routes>
          <Route path="/onboarding" element={<div>引导表单</div>} />
          <Route element={<RequireProvider />}>
            <Route path="/knowledge-bases" element={<div>知识库列表</div>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('RequireProvider', () => {
  afterEach(() => {
    getMock.mockReset()
  })

  // 回归：全新安装首跑路径。空库那次 GET 把 [] 写进了 ['providers'] 缓存，
  // 用户填完引导表单保存成功后跳回主界面——此时缓存里还是那份旧的 []，
  // 而一次重新拉取正在飞。修复前只看 isPending，这一帧就渲染
  // <Navigate to="/onboarding">，用户被打回一个字段全空的引导表单。
  it('重取还在飞时不重定向：缓存里的空数组不是"没配置过"的结论', async () => {
    let resolveProviders: (list: ProviderWithModels[]) => void = () => {}
    getMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveProviders = (list) => resolve({ data: list, error: undefined })
        }),
    )

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    queryClient.setQueryData(PROVIDERS_KEY, [])

    renderGuard(queryClient)

    // 有请求在飞 = 还没有结论：停在加载态，既不放行也不重定向。
    expect(screen.getByText('加载中…')).toBeTruthy()
    expect(screen.queryByText('引导表单')).toBeNull()

    resolveProviders([savedProvider])
    await waitFor(() => expect(screen.getByText('知识库列表')).toBeTruthy())
  })
})
