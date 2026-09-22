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

const kb = {
  id: KB_ID,
  name: '测试知识库',
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

/**
 * 两条文档，都是终态。
 *
 * 【为什么不用 queued / processing 做 fixture】useDocuments 的 refetchInterval
 * 只要看到非终态就每 2 秒重取一次，计时器会把测试进程吊住（`retry: false`
 * 管的是失败重试，管不到轮询）。要看"筛完没结果"，筛一个没人有的状态即可，
 * 不必真造一条那种文档。
 *
 * createdAt 反过来（notes 更早、坏文件更晚），而服务器的顺序是时间倒序——
 * 于是"最早在前"这个排序一定能看出行序真的变了，不是碰巧和原顺序一样。
 */
const doc: Document = {
  id: '22222222-2222-4222-8222-222222222222',
  knowledgeBaseId: KB_ID,
  filename: 'notes.md',
  status: 'ready',
  byteSize: 1024,
  chunkCount: 8,
  createdAt: '2026-09-21T00:00:00Z',
  updatedAt: '2026-09-21T00:00:00Z',
}

const failedDoc: Document = {
  id: '33333333-3333-4333-8333-333333333333',
  knowledgeBaseId: KB_ID,
  filename: '坏文件.txt',
  status: 'failed',
  byteSize: 128,
  chunkCount: 3,
  createdAt: '2026-09-21T00:00:01Z',
  updatedAt: '2026-09-21T00:00:01Z',
}

// 后端在 fail() 里对 5xx 用的是统一文案，detail 不会是内部错误原文
const internalError = {
  type: 'internal_error',
  title: '服务内部错误',
  status: 500,
  detail: '服务内部错误',
}

const invalidArgument = {
  type: 'invalid_argument',
  title: '参数不合法',
  status: 400,
  detail: 'bad id',
}

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock
const deleteMock = api.DELETE as unknown as Mock

const DOCS_PATH = '/api/v1/knowledge-bases/{id}/documents'
const SEARCH_PATH = '/api/v1/knowledge-bases/{id}/search'

/** 只把文档列表接上；需要搜索/上传的用例自己再覆写 postMock。 */
function mockDocList(docs: Document[] = [failedDoc, doc]) {
  getMock.mockImplementation(async (path: string) => {
    if (path === '/api/v1/knowledge-bases') return { data: [kb] }
    if (path === DOCS_PATH) return { data: { items: docs, nextCursor: null } }
    throw new Error(`用例没预料到的 GET ${path}`)
  })
}

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  // Toaster 是 main.tsx 里挂在路由树外面的那个，测试里照搬：
  // 页面本身不认识 toast，它只是把错误交给 useErrorToast。
  // 外面套一层 data-testid 的 div 是为了能只断言"页面这一块"——
  // sonner 不用 portal，它的 DOM 和页面在同一个 container 里。
  // 【注意】Radix 的弹窗、下拉走 portal，挂在 document.body 上，不在这个 div 里，
  // 所以"页面上没有内联渲染"这类断言用 within(page) 是准的。
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

/**
 * 等文档列表渲染出来。
 *
 * 【为什么不能再按"唯一"来找这个文件名（issue #86）】列表现在是**成对渲染**：
 * 窄屏一份卡片、`sm:` 以上一份表格，由 CSS 决定谁出现（理由见页面里的注释）。
 * jsdom 不解析 Tailwind 的类名，两份都在 DOM 里，所以 `findByText` 会撞上
 * 多个匹配直接抛错。这里要回答的问题是"数据到了没有"，不是"只有一个"——
 * `findAllBy*` 才对得上。
 */
async function waitForDocs() {
  await screen.findAllByText(doc.filename)
}

/**
 * 表格里按行列出文件名，用来断言顺序。表头那一行排掉。
 *
 * 【卡片分支不进这个函数】卡片用的是 `<li>`，没有 row 角色，所以
 * `getAllByRole('row')` 天然只拿到表格那一边——排序断言本来就是关于
 * 表格（桌面）那一套的。
 */
function rowFilenames() {
  return screen
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('cell')[0].textContent)
}

/**
 * 打开某一行的操作菜单。
 *
 * 【为什么是 pointerDown 而不是 click】Radix 的菜单触发器监听的是 pointerdown
 * （它要区分鼠标左右键），jsdom 里 click 不会合成出 pointerdown。实测过：
 * 发 pointerdown 能打开，发 click 打不开。
 *
 * 【整个文件只能用一次】打开过一次之后，同一个文件里再打开任何 Radix
 * DropdownMenu 都不再渲染内容（`@radix-ui/react-menu` 在模块作用域留了状态，
 * 换新的 root、清空 document.body 都不管用——实测过）。所以下面把删除这条
 * 路径的全部断言压在**一个**用例里，不是偷懒，是这里只能开一次。
 * AlertDialog 不受影响（可以反复打开），筛选用的是普通开关按钮，也没有这个问题。
 */
function openRowMenu(filename: string) {
  // 【限定在表格里】卡片分支有一个可访问名完全相同的菜单触发器（成对渲染，
  // 见 waitForDocs 的说明）。不限定的话 getByRole 会因为两个匹配而抛错。
  const table = within(screen.getByRole('table'))
  fireEvent.pointerDown(table.getByRole('button', { name: `${filename} 的操作` }), {
    button: 0,
  })
}

/** 弹窗里的那个按钮。Radix 的 AlertDialog 走 portal，不在 page 里。 */
function inDialog(name: string) {
  return within(screen.getByRole('alertdialog')).getByRole('button', { name })
}

afterEach(() => {
  getMock.mockReset()
  postMock.mockReset()
  deleteMock.mockReset()
  // sonner 的 toast 队列是模块级的，不清的话会漏到下一个用例里，
  // 让"只应该有一条 role=alert"之类的断言莫名其妙地红
  toast.dismiss()
  cleanup()
})

describe('文档行的状态与分块数（issue #82）', () => {
  it('状态徽标与分块数都渲染出来', async () => {
    mockDocList()
    renderPage()

    // 等表格渲染出来再断言——不然看到的是骨架屏
    await waitForDocs()

    // 【限定在表格里查】"就绪""失败"这两个词同时也是筛选开关的文案，
    // 不限定的话会遇到两个同名节点
    const table = within(screen.getByRole('table'))
    expect(table.getByText('就绪')).toBeTruthy()
    expect(table.getByText('失败')).toBeTruthy()
    // 分块数：契约里是整数不是可空，未处理完是 0，这里直接显示数字
    expect(table.getByText('8')).toBeTruthy()
    expect(table.getByText('3')).toBeTruthy()
    // 表头也得说清这一列是什么
    expect(screen.getByRole('columnheader', { name: '分块' })).toBeTruthy()
  })

  it('处理失败的文档有一个看得见的"重新索引"出路，不只是藏在菜单里', async () => {
    mockDocList()
    renderPage()

    await waitForDocs()

    // 可访问名带文件名（§14）：一串"重新索引"里要能听出是哪一行
    // 【两个分支都要有（issue #86）】表格行与卡片各渲染一份，所以是 2 个。
    // 只查一个分支的话，另一个分支漏改不会被发现——而用户看到的正是漏改的
    // 那一个（窄屏看卡片）。这条断言同时钉住了"成对渲染没有只做一半"。
    expect(screen.getAllByRole('button', { name: `重新索引 ${failedDoc.filename}` })).toHaveLength(2)
    // 就绪的那一行不该有它——这是失败态的"下一步"，不是每行都有的操作
    expect(screen.queryByRole('button', { name: `重新索引 ${doc.filename}` })).toBeNull()
  })
})

describe('删除文档（issue #91）', () => {
  /**
   * 一个用例覆盖整条路径：入口在溢出菜单里 → 确认框说明代价 → 确认后才发请求
   * → 失败的两个去处各归各位。拆成多条会是更常规的写法，但这个文件里
   * Radix 的菜单只能打开一次（见 openRowMenu 的注释），所以这里按"一条路径"
   * 来写：两次失败都发生在**同一个已经打开的弹窗**上，不涉及第二次开菜单。
   *
   * 两条失败故意选了方向相反的两个 type：
   *   internal_error   → errorPresentation(…, 'mutation') === 'toast' → 弹 toast
   *   invalid_argument → 'inline' → 必须留在弹窗里（用户改不了就没法重试）
   */
  it('行里的删除入口只在溢出菜单里；确认后才发请求，失败该 toast 的 toast、该留页面的留页面', async () => {
    mockDocList()
    deleteMock
      .mockResolvedValueOnce({ error: internalError, response: new Response() })
      .mockResolvedValueOnce({ error: invalidArgument, response: new Response() })

    renderPage()
    await waitForDocs()

    // §19：不在每行堆按钮——行里没有裸的删除按钮，只有"xxx 的操作"
    expect(screen.queryByRole('button', { name: `删除 ${doc.filename}` })).toBeNull()

    openRowMenu(doc.filename)
    const menu = screen.getByRole('menu')
    // 菜单里是两个次要操作：重新索引与删除（删除用 destructive 变体）
    expect(within(menu).getByRole('menuitem', { name: '重新索引' })).toBeTruthy()
    const deleteItem = within(menu).getByRole('menuitem', { name: '删除' })
    expect(deleteItem.getAttribute('data-variant')).toBe('destructive')
    fireEvent.click(deleteItem)

    // 【破坏性操作必须再确认一次】点了菜单项还没发请求
    const dialog = await screen.findByRole('alertdialog')
    expect(deleteMock).not.toHaveBeenCalled()
    expect(dialog.textContent).toContain(doc.filename)
    // 连带代价要写在正文里：分块与向量一起删，检索结果会变
    expect(dialog.textContent).toContain('分块与向量')

    fireEvent.click(inDialog('确定'))
    await waitFor(() =>
      expect(deleteMock).toHaveBeenCalledWith('/api/v1/documents/{id}', {
        params: { path: { id: doc.id } },
      }),
    )

    // 第一次失败（internal_error）：走 toast，页面与弹窗里都不再渲染一份，
    // 否则同一条错误会有两个 role="alert"，读屏软件念两遍。
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('服务内部错误'))
    const page = screen.getByTestId('page')
    expect(page.textContent).not.toContain('服务内部错误')
    expect(within(page).queryAllByRole('alert')).toHaveLength(0)
    expect(within(dialog).queryByText('服务内部错误')).toBeNull()
    const alertsAfterToast = screen.getAllByRole('alert').length

    // 第二次失败（invalid_argument）：要用户改东西，必须留在弹窗里，不弹 toast
    fireEvent.click(inDialog('确定'))
    await waitFor(() =>
      expect(screen.getByRole('alertdialog').textContent).toContain('提交的内容不合法'),
    )
    expect(screen.getAllByRole('alert')).toHaveLength(alertsAfterToast)
  })
})

describe('排序与筛选（issue #92）', () => {
  it('筛完没有结果时给的是"没有匹配"，不是"还没有文档"', async () => {
    mockDocList()
    renderPage()
    await waitForDocs()

    fireEvent.click(screen.getByRole('button', { name: '处理中' }))

    // 两种空态是两件事（§13/§19）：这里知识库里有文档，只是没匹配上
    expect(screen.getByText('没有「处理中」的文档')).toBeTruthy()
    expect(screen.queryByText('还没有文档')).toBeNull()
    // 出口是"清除筛选"，不是"上传第一个文档"——而且这个按钮只有一个
    // 【为什么是 queryAllBy】成对渲染之后同一个文件名在页面上有两个节点
    //（表格 + 卡片，见 waitForDocs），断言的是"一个都没有"，不是"只有一个"。
    expect(screen.queryAllByText(doc.filename)).toHaveLength(0)

    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }))
    expect(rowFilenames()).toEqual([failedDoc.filename, doc.filename])
  })

  it('筛到有匹配时只留下那一行，开关自己说明当前状态，点回"全部"恢复', async () => {
    mockDocList()
    renderPage()
    await waitForDocs()

    const chip = screen.getByRole('button', { name: '失败' })
    expect(chip.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(chip)

    // aria-pressed 是开关的语义：读屏软件念得出"已按下"（§13：状态不能只靠颜色）
    expect(screen.getByRole('button', { name: '失败' }).getAttribute('aria-pressed')).toBe('true')
    expect(rowFilenames()).toEqual([failedDoc.filename])

    fireEvent.click(screen.getByRole('button', { name: '全部' }))
    expect(rowFilenames()).toEqual([failedDoc.filename, doc.filename])
  })

  it('换排序会重排已经加载的行，但不重新发请求（游标不动，见 useDocuments 的说明）', async () => {
    mockDocList()
    renderPage()
    await waitForDocs()

    // 服务器顺序是上传时间倒序
    expect(rowFilenames()).toEqual([failedDoc.filename, doc.filename])
    const callsBeforeSort = getMock.mock.calls.length

    fireEvent.click(screen.getByRole('button', { name: '最早在前' }))

    expect(rowFilenames()).toEqual([doc.filename, failedDoc.filename])
    // 排序/筛选是纯客户端的视图变换：请求参数里没有它们，游标也就没有"串页"可言
    expect(getMock.mock.calls.length).toBe(callsBeforeSort)
  })
})

describe('检索调试视图（issue #77）', () => {
  const hit = {
    chunkId: '44444444-4444-4444-8444-444444444444',
    documentId: doc.id,
    filename: '手册.md',
    snippet: '向量检索用的是 HNSW 索引。',
    score: 0.87,
  }

  function mockSearch(result: unknown) {
    postMock.mockImplementation(async (path: string) => {
      if (path === SEARCH_PATH) return result
      throw new Error(`用例没预料到的 POST ${path}`)
    })
  }

  async function search(query: string, topK?: string) {
    await waitForDocs()
    fireEvent.change(screen.getByLabelText('查询'), { target: { value: query } })
    if (topK !== undefined) {
      fireEvent.change(screen.getByLabelText('返回条数'), { target: { value: topK } })
    }
    fireEvent.click(screen.getByRole('button', { name: '检索' }))
  }

  it('展示命中的文件名、片段与相似度，并把 topK 原样传下去', async () => {
    mockDocList()
    mockSearch({ data: { hits: [hit] }, error: undefined })
    renderPage()

    await search('向量检索用什么索引？', '10')

    expect(await screen.findByText(hit.snippet)).toBeTruthy()
    expect(screen.getByText('手册.md')).toBeTruthy()
    expect(screen.getByText('相似度 87%')).toBeTruthy()
    expect(postMock).toHaveBeenCalledWith(SEARCH_PATH, {
      params: { path: { id: KB_ID } },
      body: { query: '向量检索用什么索引？', topK: 10 },
    })
  })

  it('空命中的文案与"检索失败"分开', async () => {
    mockDocList()
    mockSearch({ data: { hits: [] }, error: undefined })
    renderPage()

    await search('知识库里没有的东西')

    expect(await screen.findByText('没有命中任何分块')).toBeTruthy()
    expect(screen.queryByText('检索失败')).toBeNull()
  })

  it('检索失败是一条页内 Alert（它是读操作，不该弹 toast）', async () => {
    mockDocList()
    mockSearch({
      error: {
        type: 'upstream_llm_error',
        title: '上游模型服务出错',
        status: 502,
        detail: 'connection refused',
      },
      response: new Response(),
    })
    renderPage()

    await search('随便问点什么')

    // 在页面这一块里，说明它是内联的 Alert，不是右下角飘过的 toast
    const page = screen.getByTestId('page')
    expect(await within(page).findByText('检索失败')).toBeTruthy()
    expect(within(page).getByText('上游模型服务出错，检查一下 API Key 和配额')).toBeTruthy()
    expect(screen.queryByText('没有命中任何分块')).toBeNull()
  })

  it('topK 越界不发请求，也不静默夹取', async () => {
    mockDocList()
    mockSearch({ data: { hits: [hit] }, error: undefined })
    renderPage()

    await search('向量检索用什么索引？', '80')

    // 夹取会让人以为"只命中这么多"，实际是被服务端截断了——契约里也写了
    // 越界返 400。所以这里连请求都不该发。
    expect(postMock).not.toHaveBeenCalled()
    expect(screen.getByText('只能填 1–50 之间的整数，留空用默认值')).toBeTruthy()
    // 提示只出现一次（提交时不再抄一份，否则同一句话在页面上两遍）
    expect(screen.getAllByText('只能填 1–50 之间的整数，留空用默认值')).toHaveLength(1)
  })
})
