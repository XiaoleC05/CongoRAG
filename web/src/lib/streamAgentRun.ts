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
 * 【首帧永远是 run_started】data 里带这次运行的 runId（新建与幂等重放
 * 都会发，形状相同）。取消、run 级重订阅、跳运行详情都要用它，而此前
 * 五种事件里没有任何一种带这个 id——所以调用方必须从这一帧取。
 *
 * 【没有自动重连,和 streamChat 同样的理由】M4-B 才做 run 维度的断线
 * 重订阅——这一轮的 POST /agents/{id}/runs 本身就是唯一能看到执行
 * 过程的连接,断了就断了,执行记录（agent_runs/agent_run_steps）仍然
 * 会落库,可以事后通过 GET /runs/{runId}/steps 查看轨迹。
 *
 * 【signal 的语义与 streamChat 完全一致】中止不算错误，不调 onError。
 * Agent 这一侧的差别在服务端：普通聊天被掐断后消息以 failed 落库，而
 * 一次运行被掐断是 interrupted——所以取消要走 cancel 端点（它才写得出
 * cancelled），本地 abort 只是"我不想再听这条流了"。
 */
export async function streamAgentRun(
  agentId: string,
  input: string,
  callbacks: StreamChatCallbacks,
  signal?: AbortSignal,
): Promise<void> {
  try {
    const resp = await fetch(`/api/v1/agents/${agentId}/runs`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ input }),
      signal,
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
    // 中止不是错误，理由见 streamChat 里同一处分支。
    if (!signal?.aborted) callbacks.onError(err)
  }
}

export type { ChatEvent }
