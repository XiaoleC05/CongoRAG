/**
 * Agent 执行的 SSE 消费——和 streamChat.ts 共用同一套帧解析器
 *（parseSSEStream/ChatEvent），只是 POST 的目标端点和请求体字段名
 * 不同：会话是 { text },Agent 运行是 { input }。
 */
import type { ChatEvent, StreamChatCallbacks } from './streamChat'
import { parseSSEStream } from './streamChat'

/**
 * 启动一次 Agent 运行并消费 SSE 流。
 *
 * 【没有自动重连,和 streamChat 同样的理由】M4-B 才做 run 维度的断线
 * 重订阅——这一轮的 POST /agents/{id}/runs 本身就是唯一能看到执行
 * 过程的连接,断了就断了,执行记录（agent_runs/agent_run_steps）仍然
 * 会落库,可以事后通过 GET /runs/{runId}/steps 查看轨迹。
 */
export async function streamAgentRun(
  agentId: string,
  input: string,
  callbacks: StreamChatCallbacks,
): Promise<void> {
  try {
    const resp = await fetch(`/api/v1/agents/${agentId}/runs`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ input }),
    })

    if (!resp.ok || !resp.body) {
      const problem = await resp.json().catch(() => null)
      callbacks.onError(problem ?? new Error(`HTTP ${resp.status}`))
      return
    }

    for await (const event of parseSSEStream(resp.body)) {
      callbacks.onEvent(event)
    }
  } catch (err) {
    callbacks.onError(err)
  }
}

export type { ChatEvent }
