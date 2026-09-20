import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@congorag/api-client'
import type { Schemas } from '@congorag/api-client'

export type Agent = Schemas['Agent']
export type AgentRun = Schemas['AgentRun']
export type AgentRunStep = Schemas['AgentRunStep']
export type ToolCatalogEntry = Schemas['ToolCatalogEntry']

export const AGENTS_KEY = ['agents']
export const toolCatalogKey = ['tool-catalog']
export const agentRunsKey = (agentId: string) => ['agent-runs', agentId]
export const runStepsKey = (runId: string) => ['run-steps', runId]

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

/** 写：创建 Agent。 */
export function useCreateAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (input: {
      name: string
      description?: string
      instruction?: string
      toolNames?: string[]
    }) => {
      const { data, error } = await api.POST('/api/v1/agents', { body: input })
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
  return useQuery({
    queryKey: agentRunsKey(agentId),
    queryFn: async () => {
      const { data, error } = await api.GET('/api/v1/agents/{id}/runs', {
        params: { path: { id: agentId } },
      })
      if (error) throw error
      return data
    },
    enabled: agentId !== '',
    refetchInterval: (query) => {
      const runs = query.state.data
      if (!runs) return false
      const stillRunning = runs.some((r) => r.status === 'pending' || r.status === 'running')
      return stillRunning ? 1500 : false
    },
  })
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
