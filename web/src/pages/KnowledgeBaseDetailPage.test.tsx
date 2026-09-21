// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { toast } from 'sonner'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { Schemas } from '@congorag/api-client'
import { api } from '@congorag/api-client'
import { Toaster } from '@/components/ui/sonner'
import KnowledgeBaseDetailPage from '@/pages/KnowledgeBaseDetailPage'

type Document = Schemas['Document']

vi.mock('@congorag/api-client', () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}))

// Toaster 的主题来自 useTheme（另一个 agent 正在改的文件）。这个用例测的是
// "错误走了哪条通道"，和主题无关——把它钉住，免得那边的改动让这里无辜变红。
vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ theme: 'dark', toggle: () => {} }),
}))

const KB_ID = '11111111-1111-4111-8111-111111111111'
const DOC_ID = '22222222-2222-4222-8222-222222222222'

const kb = {
  id: KB_ID,
  name: '测试知识库',
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

const doc: Document = {
  id: DOC_ID,
  knowledgeBaseId: KB_ID,
  filename: 'notes.md',
  // 用终态：非终态会打开 useDocuments 的轮询（queued/processing），
  // 测试里那是没必要的不确定因素
  status: 'ready',
  byteSize: 1024,
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

// 后端在 fail() 里对 5xx 用的是统一文案，detail 不会是内部错误原文
const internalError = {
  type: 'internal_error',
  title: '服务内部错误',
  status: 500,
  detail: '服务内部错误',
}

const getMock = api.GET as unknown as Mock
const deleteMock = api.DELETE as unknown as Mock

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  // Toaster 是 main.tsx 里挂在路由树外面的那个，测试里照搬：
  // 页面本身不认识 toast，它只是把错误交给 useErrorToast。
  // 外面套一层 data-testid 的 div 是为了能只断言"页面这一块"——
  // sonner 不用 portal，它的 DOM 和页面在同一个 container 里。
  return render(
    <QueryClientProvider client={queryClient}>
      <div data-testid="page">
        <MemoryRouter initialEntries={[`/knowledge-bases/${KB_ID}`]}>
          <Routes>
            <Route path="/knowledge-bases/:id" element={<KnowledgeBaseDetailPage />} />
          </Routes>
        </MemoryRouter>
      </div>
      <Toaster />
    </QueryClientProvider>,
  )
}

describe('KnowledgeBaseDetailPage 的写操作失败', () => {
  afterEach(() => {
    getMock.mockReset()
    deleteMock.mockReset()
    // sonner 的 toast 队列是模块级的，不清的话会漏到下一个用例里，
    // 让"只应该有一条 role=alert"之类的断言莫名其妙地红
    toast.dismiss()
    cleanup()
  })

  // 回归：删除文档失败（internal_error）以前只在表格下面渲染一行 ErrorText，
  // 没有任何 role="alert" 会被读屏软件念出来；现在必须走 toast。
  it('删除失败时弹出一个 role="alert" 的 toast', async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path === '/api/v1/knowledge-bases') return { data: [kb] }
      if (path === '/api/v1/knowledge-bases/{id}/documents') return { data: [doc] }
      throw new Error(`用例没预料到的 GET ${path}`)
    })
    deleteMock.mockResolvedValue({ error: internalError, response: new Response() })

    renderPage()

    // 等文档行渲染出来再点——不然点的是骨架屏
    const deleteButton = await screen.findByRole('button', { name: `删除 ${doc.filename}` })
    fireEvent.click(deleteButton)

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('服务内部错误')
    })
    expect(deleteMock).toHaveBeenCalledTimes(1)
  })

  // 一个错误只能有一个通道：toast 说过了，页面上就不能再渲染一份，
  // 否则两个 role="alert" 会各念一遍。
  it('toast 说过的错误不再在页面上内联渲染', async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path === '/api/v1/knowledge-bases') return { data: [kb] }
      if (path === '/api/v1/knowledge-bases/{id}/documents') return { data: [doc] }
      throw new Error(`用例没预料到的 GET ${path}`)
    })
    deleteMock.mockResolvedValue({ error: internalError, response: new Response() })

    renderPage()

    const deleteButton = await screen.findByRole('button', { name: `删除 ${doc.filename}` })
    fireEvent.click(deleteButton)

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('服务内部错误')
    })

    const page = screen.getByTestId('page')
    expect(page.textContent).not.toContain('服务内部错误')
    expect(within(page).queryAllByRole('alert')).toHaveLength(0)
  })

  // 反过来的那一半：not_found 在写操作里是"这条已经没了"，页面还有内容，
  // 归 toast；而 invalid_argument 要用户自己改东西，必须留在页面上。
  // 两半合起来才说明判据真的按 type + ctx 分了流。
  it('invalid_argument 走内联，不弹 toast', async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path === '/api/v1/knowledge-bases') return { data: [kb] }
      if (path === '/api/v1/knowledge-bases/{id}/documents') return { data: [doc] }
      throw new Error(`用例没预料到的 GET ${path}`)
    })
    deleteMock.mockResolvedValue({
      error: { type: 'invalid_argument', title: '参数不合法', status: 400, detail: 'bad id' },
      response: new Response(),
    })

    renderPage()

    const deleteButton = await screen.findByRole('button', { name: `删除 ${doc.filename}` })
    fireEvent.click(deleteButton)

    const page = screen.getByTestId('page')
    await waitFor(() => {
      expect(page.textContent).toContain('提交的内容不合法')
    })
    expect(within(page).queryAllByRole('alert')).toHaveLength(0)
  })
})
