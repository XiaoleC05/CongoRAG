/**
 * SSE 帧解析器，唯一规范来源是 docs/sse-protocol.md——这个文件必须照那份
 * 文档实现，不是照后端代码猜。
 *
 * 【为什么不用 EventSource】它只支持 GET、不能带自定义请求头（发消息
 * 需要能力上预留 Idempotency-Key），断线重连策略也不可控。改用
 * fetch + ReadableStream 自己解析——代价是这个文件的存在。
 */

/**
 * 和 docs/sse-protocol.md「事件类型」一节的七种事件一一对应。
 *
 * 【run_started 也在里面】它只出现在 Agent 的运行流上（普通聊天没有 Run），
 * 但帧解析器是两条流共用的，而帧是从线路上读来的——类型里没有这一档，
 * 解析器就只能靠断言把它塞进联合类型，消费端的 switch 看不到它、也不会
 * 编译报错，于是"取消按钮拿不到 runId"这类缺陷会静默成立。
 *
 * 【id 为什么是 number | null】ADR-005 记着一条硬约束：**id 可以缺席**。
 * 有两条路径会发出没有 id 的 error 帧（事件没能落库、运行根本没开始），
 * 共同点是这一帧没有对应的持久化事件。缺席和 0 是两件事：0 是后端内部的
 * "未持久化"哨兵，永远不会出现在线路上（两张计数器表都从 1 开始）。
 * 如果用 number 表示，线上那个"没有 id"会被解析成 0——一个看起来完全合法、
 * 还能参与比较的值，续传游标一旦被它推进，断线重连就会从头跳过一批还没
 * 收到的事件（见 advanceCursor）。把"没有"写进类型里，消费端就必须正面
 * 处理它，而不是碰巧忽略它。
 */
export type ChatEvent =
  | { type: 'run_started'; id: number | null; data: { runId: string } }
  | { type: 'token'; id: number | null; data: { text: string } }
  | {
      type: 'citation'
      id: number | null
      data: { chunkId: string; documentId: string; filename: string; snippet: string; score: number }
    }
  | { type: 'tool_call'; id: number | null; data: { id: string; name: string; args: unknown } }
  | { type: 'tool_result'; id: number | null; data: { id: string; result: unknown } }
  | { type: 'error'; id: number | null; data: { type: string; detail: string } }
  | { type: 'done'; id: number | null; data: Record<string, never> }

/**
 * 推进断线续传游标，返回新的游标。
 *
 * 【id 缺席的帧不参与发号】这是 ADR-005 与 docs/sse-protocol.md「帧格式」
 * 里点名的唯一例外：error 帧可能没有 id，因为那次失败恰恰发生在"分配
 * event_id"或"分配 run_id"之前。拿它当游标，下一次
 * `GET .../events?after_event_id=N` 会把 N 之后、但用户还没收到的事件
 * 一起跳过——界面上是一段凭空消失的回答，而且没有任何报错。
 *
 * 【为什么取较大值而不是直接赋值】协议只保证 event_id 单调递增，不保证
 * 连续（事务回滚会跳号），也不保证客户端按序收到。取 max 让"乱序"和
 * "重放多算了一个号"都不会把游标往回带。
 */
export function advanceCursor(cursor: number | null, event: ChatEvent): number | null {
  if (event.id === null) return cursor
  return cursor === null || event.id > cursor ? event.id : cursor
}

/**
 * 把一个 ReadableStream<Uint8Array> 解析成逐条 ChatEvent。
 *
 * 【一帧可能跨两次 read】TCP 不保证一次 read 刚好读到完整帧的边界——
 * 用一个字符串缓冲区，按 "\n\n" 切出完整帧，切不出来的部分留到下一次
 * read 再拼（docs/sse-protocol.md「帧格式」一节点名的坑）。
 *
 * 【导出给 streamAgentRun.ts 复用】M4-A 的 Agent 执行走同一套帧格式
 * 和事件类型（run_started/token/tool_call/tool_result/error/done，
 * 不出 citation；run_started 反过来只在它那边出现)——这个解析器本身和
 * "是会话还是 Agent 运行"无关,不该为了服务第二个调用方而复制一份。
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
  // null = 这一帧没有 id（ADR-005）。见 ChatEvent 上那段注释。
  let id: number | null = null
  let type = ''
  let data = ''

  for (const line of frame.split('\n')) {
    if (line.startsWith(':')) continue // 心跳注释行
    else if (line.startsWith('id: ')) {
      // 只在解出一个正整数时才认它。空值、非数字、以及 0（后端的"未持久化"
      // 哨兵，那个值整行都不会发出来）一律当作"没有 id"——把 0 收下就等于
      // 承认了一个不代表任何事件的游标值。
      const n = Number(line.slice(4))
      id = Number.isInteger(n) && n > 0 ? n : null
    } else if (line.startsWith('event: ')) type = line.slice(7)
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
  /** 【只在真的出错时调用】用户中止（signal 被 abort）不会走这里，见 streamChat。 */
  onError: (error: unknown) => void
}

/**
 * 发一条消息并消费 SSE 流。
 *
 * 【idempotencyKey 的作用】同一个会话里带同一个键重复提交时，服务端不会
 * 重新生成，而是把那一轮已经记录的事件补发一遍——帧类型、真实 event_id
 * 和顺序都与原请求一致，所以这里的解析逻辑一个字都不用改。
 *
 * 【为什么不做自动重连】docs/sse-protocol.md 定义的 after_event_id
 * 续传机制在后端已经实现并验证过（GET /conversations/{id}/events）。
 * 但"网络抖动后自动重连"是另一件事：它要维护 lastEventId、要判断重连
 * 是不是还有意义，这个文件只做一次性的流消费——连上、收完、结束；
 * 网络失败直接报给调用方。幂等键补上的正是那段缺口：用户手动重发时
 * 带上同一个键，就不会重复扣一次模型调用。
 *
 * 【空 key 不发这个头】发一个空字符串会被服务端当成"带了个空键"，
 * 而正确的语义是"这次请求不做幂等"。后端也是按空串来判的。
 *
 * 【signal 是给"停止"用的，中止不算错误】用户点停止时调用方 abort 这个
 * signal：fetch 立刻断开、正在读的流抛出 AbortError。那是用户自己的决定，
 * 不是"生成失败"——把它交给 onError，界面就会弹一句"生成失败"，与用户
 * 刚才按下的按钮正好相反。所以中止一律走正常返回路径，由调用方自己决定
 * 显示"已停止"。
 *
 * 【返回值是这次流的续传游标】最后一条**已持久化**事件的 id；一条都没有
 * （比如首帧之前就断了）时是 null。重连循环（
 * `GET /conversations/{id}/events?after_event_id=`）还没实现，但游标必须
 * 从现在起就是对的：它由 advanceCursor 算出来，id 缺席的帧推不动它。
 */
export async function streamChat(
  conversationId: string,
  text: string,
  idempotencyKey: string,
  callbacks: StreamChatCallbacks,
  signal?: AbortSignal,
): Promise<number | null> {
  let cursor: number | null = null

  try {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (idempotencyKey) {
      headers['Idempotency-Key'] = idempotencyKey
    }

    const resp = await fetch(`/api/v1/conversations/${conversationId}/messages`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text }),
      signal,
    })

    if (!resp.ok || !resp.body) {
      // 这一分支只在"请求体解析失败"之类的情况下出现（后端 handler
      // 在切到 SSE 模式之前的校验失败）——一旦切到 SSE 模式,
      // 后端保证之后的错误都通过 error 事件传递,不会再改状态码。
      const problem = await resp.json().catch(() => null)
      callbacks.onError(problem ?? new Error(`HTTP ${resp.status}`))
      return cursor
    }

    for await (const event of parseSSEStream(resp.body)) {
      // 先记游标再交给调用方：调用方可能因为一条事件直接取消订阅，
      // 而"已经收到并落库的号"不该因为那次取消而丢掉。
      cursor = advanceCursor(cursor, event)
      callbacks.onEvent(event)
    }
  } catch (err) {
    // 【判据是 signal.aborted 而不是 err.name】中止导致的异常在不同运行时
    // 里名字不一样（DOMException 的 AbortError、TypeError、以及流被关闭时
    // 的普通错误），而"我是不是被中止过"只有一个来源。
    if (!signal?.aborted) callbacks.onError(err)
  }

  return cursor
}
