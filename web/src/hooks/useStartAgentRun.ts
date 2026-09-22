import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'

import { api } from '@congorag/api-client'
import type { RunStatus } from '@/components/agent/runStatus'
import { useErrorToast } from '@/hooks/useErrorToast'
import { AGENTS_KEY, agentRunsKey } from '@/hooks/useAgents'
import { streamAgentRun } from '@/lib/streamAgentRun'

/** 时间线上的一项——文本增量累积成一条,工具调用单独一项,按到达顺序排列。 */
export type TimelineItem =
  | { kind: 'text'; content: string }
  | { kind: 'tool'; id: string; name: string; args: unknown; result?: unknown }

/**
 * 启动一次 Agent 运行 + 消费流式时间线。
 *
 * 【为什么不用 useMutation,和 useSendMessage 同样的理由】这是一个持续
 * 到 done 事件才结束的过程,中途要不断插入时间线条目——mutation 的
 * pending/error/success 三态表达不了这种中间态。
 *
 * 【tool_call 后紧跟的文本另起一项,不接着上一段文本】模型决定调用
 * 工具之后再生成的文字，语义上是"看到工具结果后的新一轮发言"，
 * 和调用工具之前的文字混在一条气泡里会让"工具调用插入流"这个交互
 * 变得含糊——前端引用方案 §2.2 点名"可折叠工具卡片插入流"，
 * 卡片前后的文字应该是断开的两段。
 *
 * 【时间线与 agent_run_steps 一一对应】后端每收到一个 tool_call 就落一行
 * Step、每步都记 latency 与 error，这里也一卡一拍：tool_call 事件建卡、
 * tool_result 事件往同一张卡上补结果（按 toolCallID 关联，不能假设
 * "上一条 tool_call 对应下一条 tool_result"这种 FIFO 顺序——模型可以一次
 * 请求多个工具）。所以"正在发生的"（这里）和"已经落库的"（RunTracePage）
 * 是同一批步骤的两种读法。
 */
export function useStartAgentRun(agentId: string) {
  const queryClient = useQueryClient()
  const showError = useErrorToast()
  const [timeline, setTimeline] = useState<TimelineItem[]>([])
  const [isRunning, setIsRunning] = useState(false)
  const [isCancelling, setIsCancelling] = useState(false)
  const [runError, setRunError] = useState<unknown>(null)
  /** 这次运行的 id——来自流的首帧 run_started，取消按钮要用它。 */
  const [runId, setRunId] = useState<string | null>(null)
  /** 本次运行的终态，只用于界面徽章；落库的权威值在"历史运行"里。 */
  const [status, setStatus] = useState<RunStatus | null>(null)

  // items 用 ref 保存——原因和 useSendMessage 的 contentRef 一样：
  // 连续的 setState 调用之间读不到彼此的最新值。
  const itemsRef = useRef<TimelineItem[]>([])
  // runId 也存一份 ref：cancel 是 useCallback，闭包里读 state 会拿到旧值。
  const runIdRef = useRef<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  /** 用户点过取消。用来把 cancelled 和"连接断了"（interrupted）分开。 */
  const cancelRequestedRef = useRef(false)
  /** 这次流走到过哪个终态。null = 没走到。 */
  const terminalRef = useRef<'done' | 'error' | null>(null)

  // 【离开页面就把这条流掐掉（issue #102）】理由和 useSendMessage 里那份
  // 一样：卸载之后事件流还在被消费，而「取消」按钮所在的页面已经没了——
  // 用户没有任何入口叫停它，只能看着它烧额度跑完。
  //
  // 【这里丢的只是"听"】服务端那次运行的归宿是运行层的决定（POST
  // /runs/{runId}/cancel），不是卸载这一刻能代替用户做的：run_started 还没
  // 到时连 runId 都没有，而且这一步没有界面能反馈结果。所以和「停止」同一条
  // 路径——中止本地流，不算错误（见 streamAgentRun 的注释）。
  useEffect(() => () => controllerRef.current?.abort(), [agentId])

  const start = useCallback(
    async (input: string) => {
      // 【进行中不许再起一次】和 useSendMessage 的同一道闸：按钮已经禁用了，
      // 但回车提交这条路径还会到这儿。
      if (controllerRef.current) return

      const controller = new AbortController()
      controllerRef.current = controller

      setIsRunning(true)
      setRunError(null)
      setTimeline([])
      setRunId(null)
      setStatus('running')
      itemsRef.current = []
      runIdRef.current = null
      cancelRequestedRef.current = false
      terminalRef.current = null

      const appendText = (text: string) => {
        const items = itemsRef.current
        const last = items[items.length - 1]
        if (last?.kind === 'text') {
          last.content += text
        } else {
          items.push({ kind: 'text', content: text })
        }
        setTimeline([...items])
      }

      try {
        await streamAgentRun(
          agentId,
          input,
          {
            onEvent: (event) => {
              switch (event.type) {
                case 'run_started':
                  // 【runId 只有这一个来源】协议里此前五种事件都不带它，
                  // 而取消（POST /runs/{runId}/cancel）、run 级重订阅、跳运行
                  // 详情都要用。首帧永远会来，取不到就说明这次运行根本没开始
                  // ——那时也没有东西可取消。
                  runIdRef.current = event.data.runId
                  setRunId(event.data.runId)
                  break
                case 'token':
                  appendText(event.data.text)
                  break
                case 'tool_call': {
                  const items = itemsRef.current
                  items.push({
                    kind: 'tool',
                    id: event.data.id,
                    name: event.data.name,
                    args: event.data.args,
                  })
                  setTimeline([...items])
                  break
                }
                case 'tool_result': {
                  const items = itemsRef.current
                  const target = items.find((it) => it.kind === 'tool' && it.id === event.data.id)
                  if (target?.kind === 'tool') {
                    target.result = event.data.result
                    setTimeline([...items])
                  }
                  break
                }
                case 'done':
                  terminalRef.current = 'done'
                  setStatus('completed')
                  break
                case 'error':
                  terminalRef.current = 'error'
                  setRunError(event.data)
                  setStatus('failed')
                  break
              }
            },
            onError: (err) => {
              terminalRef.current = 'error'
              setRunError(err)
              setStatus('failed')
            },
          },
          controller.signal,
        )
      } finally {
        // 不管这次流是怎么收场的——done 事件、error 事件、还是中途断开
        // （onError 或流没发 done 就关了）——都作废运行列表缓存：失败的那次
        // run 同样已经落库，而 useAgentRuns 只在缓存里已有 pending/running
        // 行时才轮询，不主动失效的话它要等页面重挂载才出现（乐观更新原则
        // 在这里的落地：流结束这一刻就应用，不等用户手动刷新）。
        //
        // 【本次运行的终态怎么来】协议里没有"run 状态"帧，正在跑的这一条
        // 只能在客户端按流的收场方式推导：
        //   收到 done            → completed
        //   收到 error / 连接错误 → failed
        //   用户点了取消          → cancelled（用 cancel 端点返回的那条 run
        //                          的状态，权威值，不是这里推的）
        //   流没走到终态就断了     → interrupted（客户端断开时后端写的正是它，
        //                          v4.0 起它会真的落库）
        // 徽章用的是契约里那个六态枚举，没有为它发明第二种状态名；落库的
        // 权威值仍然以"历史运行"为准，而它在下面这几行之后就重新拉一次。
        if (!cancelRequestedRef.current && terminalRef.current === null) {
          setStatus('interrupted')
        }
        setIsRunning(false)
        controllerRef.current = null
        void queryClient.invalidateQueries({ queryKey: agentRunsKey(agentId) })
        void queryClient.invalidateQueries({ queryKey: AGENTS_KEY })
      }
    },
    [agentId, queryClient],
  )

  /**
   * 取消这次在途运行（issue #79）——接后端 POST /api/v1/runs/{runId}/cancel。
   *
   * 【为什么不是本地掐断就完事】本地 abort 只是"我不想再听这条流了"，
   * 服务端那次运行还在跑、还在花用户的额度。契约里取消是运行层的动作：
   * 后端会 cancel 掉执行它的那条 context，把没跑完的那一步留 interrupted、
   * 把 run 置成 cancelled（不是 failed——两者对用户的含义完全不同）。
   * 响应里带的是**取消生效之后**的状态，所以这里直接用它，不自己推。
   */
  const cancel = useCallback(async () => {
    const id = runIdRef.current
    // run_started 还没到（首帧之前就点了取消）。按钮是禁用的，这里再挡一次：
    // 没有 id 就没有端点可发，静默什么都不做比发一个假请求好。
    if (id === null) return

    setIsCancelling(true)
    try {
      const { data, error } = await api.POST('/api/v1/runs/{runId}/cancel', {
        params: { path: { runId: id } },
      })

      if (error) {
        // 409：这条 run 已经到终态，取消不了。写操作失败按 §16 走
        // errorPresentation 判定的呈现方式（conflict 落到 toast），同时把
        // 列表拉一次——用户点取消的意图是"我不想再等了"，那就该看到它现在
        // 到底是什么状态，而不是一条错误。
        if (showError(error)) setRunError(error)
        void queryClient.invalidateQueries({ queryKey: agentRunsKey(agentId) })
        return
      }

      cancelRequestedRef.current = true
      setStatus(data.status)

      // 【取消成功后把本地这条流也掐掉】取消端点返回时后端已经收尾完成
      // （它会等这次运行的终结写入），后续事件不会再有任何意义。等着服务端
      // 把连接关掉会让"取消"看起来没生效。掐断走的是和中止同一条路径——
      // 不算错误，不推 runError（见 streamAgentRun 的注释）。
      controllerRef.current?.abort()
    } finally {
      setIsCancelling(false)
    }
  }, [agentId, queryClient, showError])

  return { start, cancel, isRunning, isCancelling, timeline, runError, runId, status }
}
