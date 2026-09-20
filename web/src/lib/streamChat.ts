/**
 * SSE 帧解析器，唯一规范来源是 docs/sse-protocol.md——这个文件必须照那份
 * 文档实现，不是照后端代码猜。
 *
 * 【为什么不用 EventSource】它只支持 GET、不能带自定义请求头（发消息
 * 需要能力上预留 Idempotency-Key），断线重连策略也不可控。改用
 * fetch + ReadableStream 自己解析——代价是这个文件的存在。
 */

/** 和 docs/sse-protocol.md「事件类型」一节的六种事件一一对应。 */
export type ChatEvent =
  | { type: 'token'; id: number; data: { text: string } }
  | {
      type: 'citation'
      id: number
      data: { chunkId: string; documentId: string; filename: string; snippet: string; score: number }
    }
  | { type: 'tool_call'; id: number; data: { id: string; name: string; args: unknown } }
  | { type: 'tool_result'; id: number; data: { id: string; result: unknown } }
  | { type: 'error'; id: number; data: { type: string; detail: string } }
  | { type: 'done'; id: number; data: Record<string, never> }

/**
 * 把一个 ReadableStream<Uint8Array> 解析成逐条 ChatEvent。
 *
 * 【一帧可能跨两次 read】TCP 不保证一次 read 刚好读到完整帧的边界——
 * 用一个字符串缓冲区，按 "\n\n" 切出完整帧，切不出来的部分留到下一次
 * read 再拼（docs/sse-protocol.md「帧格式」一节点名的坑）。
 *
 * 【导出给 streamAgentRun.ts 复用】M4-A 的 Agent 执行走同一套帧格式
 * 和事件类型（token/tool_call/tool_result/error/done，只是不会出现
 * citation)——这个解析器本身和"是会话还是 Agent 运行"无关,不该为了
 * 服务第二个调用方而复制一份。
 */
export async function* parseSSEStream(body: ReadableStream<Uint8Array>): AsyncGenerator<ChatEvent> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })

      let frameEnd: number
      while ((frameEnd = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, frameEnd)
        buffer = buffer.slice(frameEnd + 2)
        const event = parseFrame(frame)
        if (event) yield event
      }
    }
  } finally {
    reader.releaseLock()
  }
}

/** 解析一帧："id: N\nevent: xxx\ndata: {...}"，忽略以 ":" 开头的心跳注释行。 */
function parseFrame(frame: string): ChatEvent | null {
  let id = 0
  let type = ''
  let data = ''

  for (const line of frame.split('\n')) {
    if (line.startsWith(':')) continue // 心跳注释行
    if (line.startsWith('id: ')) id = Number(line.slice(4))
    else if (line.startsWith('event: ')) type = line.slice(7)
    else if (line.startsWith('data: ')) data = line.slice(6)
  }

  if (!type) return null

  let parsed: unknown
  try {
    parsed = JSON.parse(data)
  } catch {
    return null // 解析不出来的帧丢弃，不让一帧坏数据打断整个流
  }

  // 后端把 {type, data} 整个当 payload 存（见 conversation.emitEvent），
  // 所以 SSE 的 event 字段和 payload 里的 type 字段是同一个值，
  // 这里统一用 event 字段（它更早就位，且不依赖 payload 结构）。
  const body = (parsed as { data?: unknown })?.data ?? {}
  return { type, id, data: body } as ChatEvent
}

export type StreamChatCallbacks = {
  onEvent: (event: ChatEvent) => void
  onError: (error: unknown) => void
}

/**
 * 发一条消息并消费 SSE 流。
 *
 * 【为什么不做自动重连】docs/sse-protocol.md 定义的 after_event_id
 * 续传机制在后端已经实现并验证过（GET /conversations/{id}/events）,
 * 但"网络抖动后自动重连"这件事和 M4-B 的幂等键/断线重订阅是同一块——
 * 那块工作范围本身不在这一轮里（见 helperDoc 的范围划分）。这里只做
 * 一次性的流消费：连上、收完、结束；网络失败直接报给调用方，
 * 由用户决定要不要重新发一遍消息。
 */
export async function streamChat(
  conversationId: string,
  text: string,
  callbacks: StreamChatCallbacks,
): Promise<void> {
  try {
    const resp = await fetch(`/api/v1/conversations/${conversationId}/messages`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    })

    if (!resp.ok || !resp.body) {
      // 这一分支只在"请求体解析失败"之类的情况下出现（后端 handler
      // 在切到 SSE 模式之前的校验失败）——一旦切到 SSE 模式,
      // 后端保证之后的错误都通过 error 事件传递,不会再改状态码。
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
