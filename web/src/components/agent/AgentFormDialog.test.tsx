// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

import type { Schemas } from '@congorag/api-client'
import { api } from '@congorag/api-client'
import { AgentFormDialog } from '@/components/agent/AgentFormDialog'

type Agent = Schemas['Agent']
type ProviderWithModels = Schemas['ProviderWithModels']
type ToolCatalogEntry = Schemas['ToolCatalogEntry']

vi.mock('@congorag/api-client', () => ({
  api: { GET: vi.fn(), POST: vi.fn(), PATCH: vi.fn() },
}))

const getMock = api.GET as unknown as Mock
const postMock = api.POST as unknown as Mock
const patchMock = api.PATCH as unknown as Mock

const AGENT_ID = '11111111-1111-4111-8111-111111111111'

const agent: Agent = {
  id: AGENT_ID,
  name: '算数助手',
  description: '只做算术',
  instruction: '你是一个算术助手。',
  toolNames: ['calculator'],
  createdAt: '2026-09-22T00:00:00Z',
  updatedAt: '2026-09-22T00:00:00Z',
}

/**
 * 工具目录三个等级各来一个。
 *
 * 【为什么三个都要有】契约里 sideEffectLevel 是必填的三值枚举，而用户勾选时
 * 真正要看的是"这个工具会不会写东西"。只放 READ_ONLY 的话，"三个等级都能
 * 显示出来"这件事没有被任何断言钉住。
 */
const tools: ToolCatalogEntry[] = [
  {
    name: 'calculator',
    description: '做基础算术运算',
    sideEffectLevel: 'READ_ONLY',
  },
  {
    name: 'note_writer',
    description: '往笔记里追加一条',
    sideEffectLevel: 'WRITE_IDEMPOTENT',
  },
  {
    name: 'mail_sender',
    description: '发一封邮件',
    sideEffectLevel: 'WRITE_NON_IDEMPOTENT',
  },
]

/** 当前生效的 chat 模型（同 kind 里 createdAt 最新的那一条）。 */
const providers: ProviderWithModels[] = [
  {
    id: '22222222-2222-4222-8222-222222222222',
    baseUrl: 'https://api.example.com/v1',
    createdAt: '2026-09-21T00:00:00Z',
    models: [
      {
        id: '33333333-3333-4333-8333-333333333333',
        modelId: 'gpt-4o-mini',
        kind: 'chat',
        capabilities: { chat: true, streaming: true, toolCalling: true, reasoning: false },
        contextWindow: 128000,
        maxOutputTokens: 4096,
        tokenizerType: 'cl100k_base',
        embeddingDim: 0,
        createdAt: '2026-09-21T00:00:00Z',
      },
    ],
  },
]

function mockCatalog() {
  getMock.mockImplementation(async (path: string) => {
    if (path === '/api/v1/tools') return { data: tools, error: undefined }
    if (path === '/api/v1/providers') return { data: providers, error: undefined }
    throw new Error(`用例没预料到的 GET ${path}`)
  })
}

/** 打开弹窗（editAgent 传了就是编辑模式）。 */
function renderDialog(editAgent?: Agent) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/agents/${AGENT_ID}`]}>
        <AgentFormDialog open onOpenChange={() => {}} agent={editAgent} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  getMock.mockReset()
  postMock.mockReset()
  patchMock.mockReset()
  cleanup()
})

/**
 * jsdom 里没有 ResizeObserver。
 *
 * 【为什么非要有这个桩】Radix 的 Checkbox 内部会渲染一个隐藏的原生输入
 * （BubbleInput）来参与表单提交，它靠 `@radix-ui/react-use-size` 测量尺寸，
 * 那个 hook 直接 `new ResizeObserver(...)`。jsdom 没实现它，于是抛
 * `ReferenceError: ResizeObserver is not defined`——**这一抛是在提交阶段**，
 * React 会把整棵子树卸掉，测试里看到的现象不是"复选框没渲染"，而是
 * "弹窗里什么都没有了"（连"生效的模型"这种无关的断言一起红）。
 * 这个坑 README 没记，第一次在 jsdom 里渲染 shadcn 的 Checkbox 就会撞上。
 */
beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

/**
 * 取某个工具的勾选框。
 *
 * 【为什么不写 getByRole('checkbox', { name: /calculator/ })】勾选框在
 * `<label>` 里，而那个 label 的可访问名是"工具名 + 副作用等级 + 描述"拼起来
 * 的一整串（还带一个 Badge）——按角色 + 名字查会依赖这段拼接的确切形状。
 * 这里改成"先按工具名找到那一行，再在行里找唯一的勾选框"，改文案不会误伤。
 */
function toolCheckbox(toolName: string) {
  const row = screen.getByText(toolName).closest('label')
  if (!row) throw new Error(`找不到 ${toolName} 那一行`)
  return within(row as HTMLElement).getByRole('checkbox')
}

describe('Agent 配置表单（issue #81）', () => {
  it('名字 / 系统提示词 / 工具多选都在，工具的副作用等级也显示出来', async () => {
    mockCatalog()
    renderDialog()

    expect(screen.getByLabelText('名字')).toBeTruthy()
    expect(screen.getByLabelText('系统提示词（可选）')).toBeTruthy()

    // 工具选项来自 GET /api/v1/tools，每个都带"会不会写东西"这一位
    expect(await screen.findByText('calculator')).toBeTruthy()
    expect(screen.getByText('只读')).toBeTruthy()
    expect(screen.getByText('写入（可重复）')).toBeTruthy()
    expect(screen.getByText('写入（不可重复）')).toBeTruthy()
  })

  it('把当前生效的模型只读显示出来，并指向设置页（Agent 没有 per-agent 模型绑定）', async () => {
    mockCatalog()
    renderDialog()

    expect(await screen.findByText('生效的模型')).toBeTruthy()
    // 模型名要等 providers 拉回来（异步），所以用 findByText
    expect(await screen.findByText('gpt-4o-mini')).toBeTruthy()
    // 说明"所有 Agent 共用它、改它去设置页"，并且给的是真链接
    expect(screen.getByText(/所有 Agent 共用它/)).toBeTruthy()
    const link = screen.getByRole('link', { name: '设置页' })
    expect(link.getAttribute('href')).toBe('/settings')
    // 这一项不能是选择框（combobox/listbox）：契约里 Agent 没有模型字段，
    // 选了也存不进去，做一个下拉就是在骗人
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).queryByRole('combobox')).toBeNull()
  })

  it('系统提示词留空是合法的，并如实说明"没有系统提示词"而不是"用默认的"', async () => {
    mockCatalog()
    renderDialog()

    const instruction = screen.getByLabelText('系统提示词（可选）') as HTMLTextAreaElement
    expect(instruction.value).toBe('')
    // 名字填了、提示词空着 → 可以直接提交（后端只查上限，
    // ADK 在 instruction 为空时不追加 system 消息）
    fireEvent.change(screen.getByLabelText('名字'), { target: { value: '算术助手' } })
    expect((screen.getByRole('button', { name: '创建' }) as HTMLButtonElement).disabled).toBe(
      false,
    )
    expect(screen.getByText(/后端不会替你补一段默认的/)).toBeTruthy()
  })

  it('名字为空时提交按钮禁用，并给出原因', async () => {
    mockCatalog()
    renderDialog()

    expect(screen.getByText('名字不能为空')).toBeTruthy()
    expect((screen.getByRole('button', { name: '创建' }) as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('名字'), { target: { value: '检索助手' } })
    expect(screen.queryByText('名字不能为空')).toBeNull()
    expect((screen.getByRole('button', { name: '创建' }) as HTMLButtonElement).disabled).toBe(
      false,
    )
  })

  it('新建走 POST，四个字段一起发（含勾选的工具）', async () => {
    mockCatalog()
    postMock.mockResolvedValue({ data: agent, error: undefined })
    renderDialog()

    fireEvent.change(screen.getByLabelText('名字'), { target: { value: '检索助手' } })
    fireEvent.change(screen.getByLabelText('描述（可选）'), { target: { value: '查知识库' } })
    fireEvent.change(screen.getByLabelText('系统提示词（可选）'), {
      target: { value: '先检索再回答。' },
    })
    await screen.findByText('calculator') // 工具目录还没回来时勾选框不在
    expect(toolCheckbox('calculator').getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toolCheckbox('calculator'))
    fireEvent.click(screen.getByRole('button', { name: '创建' }))

    // 【为什么用 waitFor】mutation 不是同步发出去的：TanStack 在微任务里
    // 才执行 mutationFn，点击之后立刻断言会看到 0 次调用（实测过）。
    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith('/api/v1/agents', {
        body: {
          name: '检索助手',
          description: '查知识库',
          instruction: '先检索再回答。',
          toolNames: ['calculator'],
        },
      }),
    )
  })
})

describe('编辑已有 Agent（issue #81 的"不只是新建"）', () => {
  it('初始值就是这条 Agent 的当前配置', async () => {
    mockCatalog()
    renderDialog(agent)

    expect((screen.getByLabelText('名字') as HTMLInputElement).value).toBe('算数助手')
    expect((screen.getByLabelText('系统提示词（可选）') as HTMLTextAreaElement).value).toBe(
      '你是一个算术助手。',
    )
    await screen.findByText('calculator')
    expect(toolCheckbox('calculator').getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('button', { name: '保存' })).toBeTruthy()
  })

  it('保存走 PATCH，请求体是四个字段的整体替换（清空提示词也表达得出来）', async () => {
    mockCatalog()
    patchMock.mockResolvedValue({ data: { ...agent, instruction: '' }, error: undefined })
    renderDialog(agent)

    fireEvent.change(screen.getByLabelText('系统提示词（可选）'), { target: { value: '' } })
    await screen.findByText('calculator')
    fireEvent.click(toolCheckbox('calculator')) // 取消勾选
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    // instruction / toolNames 是空串和空数组，不是"省略"——契约里它们 required，
    // 省略会被（整体替换语义的）后端当成清空。这里明确地把"清空"发出去。
    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith('/api/v1/agents/{id}', {
        params: { path: { id: AGENT_ID } },
        body: {
          name: '算数助手',
          description: '只做算术',
          instruction: '',
          toolNames: [],
        },
      }),
    )
  })

  it('模型不支持工具调用时，已经勾上的工具仍然可以取消（否则是死局）', async () => {
    // 生效的模型没有声明 toolCalling，而这个 Agent 已经绑了 calculator
    getMock.mockImplementation(async (path: string) => {
      if (path === '/api/v1/tools') return { data: tools, error: undefined }
      if (path === '/api/v1/providers') {
        return {
          data: [
            {
              ...providers[0],
              models: [
                {
                  ...providers[0].models[0],
                  capabilities: {
                    chat: true,
                    streaming: true,
                    toolCalling: false,
                    reasoning: false,
                  },
                },
              ],
            },
          ],
          error: undefined,
        }
      }
      throw new Error(`用例没预料到的 GET ${path}`)
    })
    renderDialog(agent)

    await screen.findByText('calculator')
    // 后端的门控是"提交的工具集非空"才查模型能力——移除工具不需要这个能力。
    // 已勾上的必须能取消，否则用户被永久锁在一个保存就被 400 的配置上。
    expect((toolCheckbox('calculator') as HTMLButtonElement).disabled).toBe(false)
    // 没勾上的仍然禁用：勾上去只会被服务端拒
    expect((toolCheckbox('note_writer') as HTMLButtonElement).disabled).toBe(true)
  })

  it('提交中按钮禁用并改文案，防重复提交（§16）', async () => {
    mockCatalog()
    // 一个不会 resolve 的 promise：让"提交中"这一帧停在界面上
    patchMock.mockImplementation(() => new Promise(() => {}))
    renderDialog(agent)

    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    const saving = await screen.findByRole('button', { name: '保存中…' })
    expect((saving as HTMLButtonElement).disabled).toBe(true)
    // 按钮已经禁用，再点一次不会发出第二个请求
    fireEvent.click(saving)
    expect(patchMock).toHaveBeenCalledTimes(1)
  })

  it('服务端拒绝时错误贴在弹窗里（invalid_argument 要用户改内容，不能只弹 toast）', async () => {
    mockCatalog()
    patchMock.mockResolvedValue({
      error: {
        type: 'invalid_argument',
        title: '参数不合法',
        status: 400,
        detail: 'agent instruction is too long',
      },
    })
    renderDialog(agent)

    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('提交的内容不合法')).toBeTruthy()
  })
})
