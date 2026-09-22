import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'
import { RUN_STATUS_LABEL } from '@/components/agent/runStatus'
import type { RunStatus } from '@/components/agent/runStatus'
import { flattenPages, nextPageParam } from '@/lib/pagination'

export type Agent = Schemas['Agent']
export type AgentRun = Schemas['AgentRun']
export type AgentRunStep = Schemas['AgentRunStep']
export type ToolCatalogEntry = Schemas['ToolCatalogEntry']

export const AGENTS_KEY = ['agents']
export const toolCatalogKey = ['tool-catalog']
export const agentRunsKey = (agentId: string) => ['agent-runs', agentId]
export const runStepsKey = (runId: string) => ['run-steps', runId]

/**
 * 系统提示词的长度上限（契约里 `instruction` 的 maxLength）。
 *
 * 【和名字一样有三处必须一致，没有代码生成能同步】
 *
 *   1. internal/agent/usecase.go 的 maxInstructionLen（真正的强制点）
 *   2. contracts/openapi.yaml     的 maxLength
 *   3. 这里                        （提交前的客户端校验）
 *
 * 同样按**字符数**算（`len([]rune(...))` 对 `[...name].length`）。
 */
export const MAX_INSTRUCTION_LEN = 4000

/** Agent 描述的长度上限。三处同源，见上面 MAX_INSTRUCTION_LEN 的说明。 */
export const MAX_DESCRIPTION_LEN = 2000

/**
 * 系统提示词的客户端校验。返回 null 表示通过。
 *
 * 【为什么空串是合法的】这是 issue #81 点名要写清的那一条，依据是后端的两段代码：
 *
 *   - `internal/agent/usecase.go` 的 `validateAgentFields` 对 instruction **只查上限**
 *     （`if n := len([]rune(instruction)); n > maxInstructionLen`），空串不报错；
 *   - `eino_adk.go:174` 把它原样交给 ADK 的 `ChatModelAgentConfig.Instruction`，
 *     而 Eino 的默认输入构造函数（`adk/chatmodel.go` 的 `defaultGenModelInput`）
 *     是 `if instruction != "" { msgs = append(msgs, schema.SystemMessage(instruction)) }`。
 *
 * 也就是说：**留空 = 这次请求里没有 system 消息**，不存在"后端补一段默认提示词"
 * 这种行为。所以这里返回 null（放行），表单上要如实告诉用户留空意味着什么——
 * 报错会凭空造出一条后端没有的约束，而显示"已使用默认提示词"是在编事实。
 */
export function validateInstruction(raw: string): string | null {
  if ([...raw].length > MAX_INSTRUCTION_LEN) {
    return `系统提示词不能超过 ${MAX_INSTRUCTION_LEN} 个字`
  }
  return null
}

/** Agent 描述的客户端校验。返回 null 表示通过。空串合法（描述本来就是可选的）。 */
export function validateDescription(raw: string): string | null {
  if ([...raw].length > MAX_DESCRIPTION_LEN) {
    return `描述不能超过 ${MAX_DESCRIPTION_LEN} 个字`
  }
  return null
}

/** 读：工具目录（创建 Agent 表单的勾选列表）。 */
export function useToolCatalog() {
  return useQuery({
    queryKey: toolCatalogKey,
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/tools')
      if (error) throw error
      return data
    },
  })
}

/** 读：Agent 列表。 */
export function useAgents() {
  return useQuery({
    queryKey: AGENTS_KEY,
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/agents')
      if (error) throw error
      return data
    },
  })
}

/** 读：单个 Agent。 */
export function useAgent(agentId: string) {
  return useQuery({
    queryKey: [...AGENTS_KEY, agentId],
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/agents/{id}', {
        params: { path: { id: agentId } },
      })
      if (error) throw error
      return data
    },
    enabled: agentId !== '',
  })
}

/**
 * 写：创建 Agent。
 *
 * 【入参类型直接用契约的 CreateAgentRequest】README §5：手写一个"和后端一样的"
 * 结构迟早会过期，而且过期不报错——契约里加了字段，手写的那个还是旧的。
 */
export function useCreateAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (input: Schemas['CreateAgentRequest']) => {
      const { data, error } = await api.POST('/api/v1/agents', { body: input })
      if (error) throw error
      return data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: AGENTS_KEY }),
  })
}

/**
 * 写：改一个 Agent 的配置（issue #81）。
 *
 * 【为什么是 PATCH 而不是 PUT，以及为什么请求体是四个必填字段】
 * 契约里那个 operation 写明了：**整体替换，不是字段级合并**。四个字段
 * （name / description / instruction / toolNames）全部 required，语义是
 * "改完之后这个 Agent 是什么"。字段级合并会让"把 system prompt 清空"
 * 表达不出来——没给该字段与"给了空串"在合并语义下长得一样。
 *
 * 【失效范围】AGENTS_KEY 是 ['agents']，前缀失效会连带作废单条
 * （['agents', id]），所以详情页改完名字会立刻跟着变（§3）。
 */
export function useUpdateAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (input: { id: string; body: Schemas['UpdateAgentRequest'] }) => {
      const { data, error } = await api.PATCH('/api/v1/agents/{id}', {
        params: { path: { id: input.id } },
        body: input.body,
      })
      if (error) throw error
      return data
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: AGENTS_KEY }),
  })
}

/**
 * 读：一个 Agent 的历史执行记录。
 *
 * 【轮询,和 useDocuments 同样的理由】一次运行是异步流式过程,列表页
 * 需要在运行进行中时也能看到 currentStep 往前走,不能只在打开页面
 * 那一刻拉一次——只要还有非终态（pending/running）的 run 就继续轮询。
 */
export function useAgentRuns(agentId: string) {
  return useInfiniteQuery({
    queryKey: agentRunsKey(agentId),
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET('/api/v1/agents/{id}/runs', {
        params: { path: { id: agentId }, query: { cursor: pageParam ?? undefined } },
      })
      if (error) throw error
      return data
    },
    getNextPageParam: nextPageParam,
    enabled: agentId !== '',
    // 轮询判据不变，只是要在摊平之后的集合上判（理由同 useDocuments）。
    refetchInterval: (query) => {
      const data = query.state.data
      if (!data) return false
      const stillRunning = flattenPages(data).some(
        (r) => r.status === 'pending' || r.status === 'running',
      )
      return stillRunning ? 1500 : false
    },
  })
}

/** 运行历史的排序方式（issue #92 的另一半）。 */
export type RunSort = 'newest' | 'oldest'

/** 运行历史的筛选方式（issue #92 的另一半）。'all' = 不筛。 */
export type RunFilter = 'all' | RunStatus

/**
 * 筛选 / 排序的文案与取值顺序。
 *
 * 【六态的文案不在这里再写一份】README §17：run 状态的中文映射全站只有
 * runStatus.ts 那一份。这里把 `all` 拼在它前面，而不是抄六行中文——
 * 抄一份的下场是两处迟早不一样，而且不报错，只是同一个状态在筛选开关上
 * 叫一个名字、在徽章上叫另一个。
 *
 * 【两张 Record 表把"漏了"变成编译错误】契约加第七态时，`RunFilter`
 * 跟着变，这张表就会报缺项。顺序表（RUN_FILTERS）不会——它声明成
 * `RunFilter[]` 只是"元素合法"，加状态的人记得回来加一行。
 */
export const RUN_FILTER_LABEL: Record<RunFilter, string> = {
  all: '全部',
  ...RUN_STATUS_LABEL,
}

export const RUN_SORT_LABEL: Record<RunSort, string> = {
  newest: '最新在前',
  oldest: '最早在前',
}

/**
 * 筛选开关的顺序。照状态机走：还在动的两个（pending/running）在前，
 * interrupted 在最后——它是最少见的一态，筛它的人心里已经有数。
 */
export const RUN_FILTERS: RunFilter[] = [
  'all',
  'pending',
  'running',
  'completed',
  'failed',
  'cancelled',
  'interrupted',
]

export const RUN_SORTS: RunSort[] = ['newest', 'oldest']

/**
 * 运行创建的毫秒时间戳。解析不了时给 0，让坏数据沉到最旧那一端，
 * 而不是把整个排序搞乱（NaN 参与比较会让顺序变得不可预测）。
 */
const runCreatedAt = (run: AgentRun) => {
  const t = new Date(run.createdAt).getTime()
  return Number.isNaN(t) ? 0 : t
}

/**
 * 运行历史的排序 + 筛选（issue #92 的另一半）。
 *
 * 【为什么是客户端做】`GET /api/v1/agents/{id}/runs` 的查询参数只有
 * `limit` / `cursor`，没有排序与筛选参数。加参数要先动
 * `contracts/openapi.yaml`（本批次不动），所以这里是纯客户端的视图变换
 * ——发出去的请求一个字节都没变。
 *
 * 【数据规模上限的假设】单个 Agent 的历史 run 是几十条的量级、每页 50 条，
 * 在内存里排一遍可以忽略。**这个前提不成立时方案就废了**：没翻过的页不参与
 * 排序与筛选，用户会把"筛不出来"当成"本来就没有"——所以工具条上要写
 * "已加载 N 条"，空态也要说清是在多少条里筛的。
 *
 * 【它和游标的关系（#92 点名要写清的那条）】游标编码的是"服务器顺序
 * （created_at 倒序）里的位置"。排序与筛选不改请求参数，所以游标在排序
 * 前后指的是同一个东西，**不存在"拿着旧排序的位置去取新排序的数据"这种
 * 串页**，也不需要重置游标。**将来若把排序挪到服务端，排序变更时必须把
 * 游标清回 null**——那一刻游标才开始编码排序结果里的位置，不清就是串页。
 * 这一条与文档列表（useDocuments.ts 的 sortAndFilterDocuments）是同一个
 * 结论、同一套做法。
 *
 * 【一个可见的后果，如实说在这里】"最早在前"只重排**已经加载**的那些页；
 * 点了「加载更多」之后，新拉到的更早的 run 会插到列表最前面。这不是 bug，
 * 是"客户端排序 + 服务端分页"的必然形状。
 */
export function sortAndFilterRuns(
  runs: AgentRun[],
  sort: RunSort,
  filter: RunFilter,
): AgentRun[] {
  const filtered = filter === 'all' ? runs : runs.filter((run) => run.status === filter)

  // 默认顺序就是服务器给的顺序（created_at 倒序），不必在客户端再排一遍。
  if (sort === 'newest') return filtered

  // 【按时间戳比，不按字符串比】契约里的时间是带时区偏移的 ISO 串，
  // 字符串比较在不同偏移之间会得出错误顺序，而且不会报错——和
  // web/README.md §11 不切片日期是同一类坑。
  return [...filtered].sort((a, b) => runCreatedAt(a) - runCreatedAt(b))
}

/** 读：一次执行的全部步骤（执行轨迹页）。 */
export function useRunSteps(runId: string) {
  return useQuery({
    queryKey: runStepsKey(runId),
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/runs/{runId}/steps', {
        params: { path: { runId } },
      })
      if (error) throw error
      return data
    },
    enabled: runId !== '',
  })
}
