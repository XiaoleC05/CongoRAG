import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef, useState } from 'react'

import { streamAgentRun } from '@/lib/streamAgentRun'
import { AGENTS_KEY, agentRunsKey } from '@/hooks/useAgents'

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
 */
export function useStartAgentRun(agentId: string) {
  const queryClient = useQueryClient()
  const [timeline, setTimeline] = useState<TimelineItem[]>([])
  const [isRunning, setIsRunning] = useState(false)
  const [runError, setRunError] = useState<unknown>(null)

  // items 用 ref 保存——原因和 useSendMessage 的 contentRef 一样：
  // 连续的 setState 调用之间读不到彼此的最新值。
  const itemsRef = useRef<TimelineItem[]>([])

  const start = useCallback(
    async (input: string) => {
      setIsRunning(true)
      setRunError(null)
      setTimeline([])
      itemsRef.current = []

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
        await streamAgentRun(agentId, input, {
          onEvent: (event) => {
            switch (event.type) {
              case 'token':
                appendText(event.data.text)
                break
              case 'tool_call': {
                const items = itemsRef.current
                items.push({ kind: 'tool', id: event.data.id, name: event.data.name, args: event.data.args })
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
              case 'error':
                setRunError(event.data)
                break
            }
          },
          onError: (err) => {
            setRunError(err)
          },
        })
      } finally {
        // 不管这次流是怎么收场的——done 事件、error 事件、还是中途断开
        // （onError 或流没发 done 就关了）——都作废运行列表缓存：失败的那次
        // run 同样已经落库，而 useAgentRuns 只在缓存里已有 pending/running
        // 行时才轮询，不主动失效的话它要等页面重挂载才出现（乐观更新原则
        // 在这里的落地：流结束这一刻就应用，不等用户手动刷新）。
        queryClient.invalidateQueries({ queryKey: agentRunsKey(agentId) })
        queryClient.invalidateQueries({ queryKey: AGENTS_KEY })
        setIsRunning(false)
      }
    },
    [agentId, queryClient],
  )

  return { start, isRunning, timeline, runError }
}
