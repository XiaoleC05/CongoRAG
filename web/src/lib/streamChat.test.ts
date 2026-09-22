import { afterEach, describe, expect, it, vi } from 'vitest'

import { advanceCursor, streamChat } from '@/lib/streamChat'
import type { ChatEvent } from '@/lib/streamChat'

// 跑在默认的 node 环境里：这个文件测的是纯解析与游标推进，不需要 DOM。
// ReadableStream / TextEncoder / AbortController 都是 Node 自带的全局量。

/**
 * 拼一帧的线路格式。
 *
 * 【data 里是整个 {type, data}——不是只有 data】后端把 {type, data} 整体
 * 序列化成 payload 再写进 data 行（apps/api/internal/api/sse.go 的 Emit），
 * 解析器也是按这个形状拆的。照 docs/sse-protocol.md 那份「事件类型」一节
 * 的示例（只写 data）拼，会得到一帧解析不出内容的数据——测试就白测了。
 *
 * 【id 为 null 时不写 id 行】这正是 ADR-005 说的那条路径：这帧没有对应的
 * 持久化事件，所以没有号可发。
 */
function frame(id: number | null, type: string, data: unknown): string {
  const head = id === null ? '' : `id: ${id}\n`
  return `${head}event: ${type}\ndata: ${JSON.stringify({ type, data })}\n\n`
}

/**
 * 把一批帧接成一个 ReadableStream，并把它装成 fetch 的响应。
 *
 * 【holdOpen 是给中止用例用的】流式连接的特点就是"帧发完了还开着"。
 * 不模拟这一点，中止测的就不是"中止一条还活着的流"，而是"中止一条已经
 * 结束的流"——后者什么也证明不了。
 */
function mockFetchChunks(chunks: string[], options: { signal?: AbortSignal; holdOpen?: boolean } = {}) {
  const encoder = new TextEncoder()
  let i = 0

  const body = new ReadableStream<Uint8Array>({
    async pull(controller) {
      if (i < chunks.length) {
        controller.enqueue(encoder.encode(chunks[i]))
        i += 1
        return
      }

      if (!options.holdOpen) {
        controller.close()
        return
      }

      // 帧发完了，连接还开着：等中止信号（真实场景里是等服务端发下一条）。
      await new Promise<void>((resolve) => {
        if (options.signal?.aborted) return resolve()
        options.signal?.addEventListener('abort', () => resolve(), { once: true })
      })
      controller.error(new DOMException('aborted', 'AbortError'))
    },
  })

  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({ ok: true, body }) as unknown as Response),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('advanceCursor（ADR-005）', () => {
  // 【这条就是 ADR-005 里那句"不能因为收到它就推进续传游标"】没有 id 的
  // error 帧不代表任何已持久化的事件，拿它当游标会让断线续传从头跳过一批
  // 还没收到的事件——界面上是一段凭空消失的回答，而且不报错。
  it('id 缺席的帧不推进游标', () => {
    const err: ChatEvent = {
      type: 'error',
      id: null,
      data: { type: 'internal_error', detail: '事件没能落库' },
    }
    expect(advanceCursor(7, err)).toBe(7)
    // 连游标都还没有的时候也不能凭空造一个出来
    expect(advanceCursor(null, err)).toBeNull()
  })

  it('有 id 的帧推进游标，且只增不减（乱序与重放都不会把它带回去）', () => {
    const token = (id: number): ChatEvent => ({ type: 'token', id, data: { text: 'x' } })
    expect(advanceCursor(null, token(1))).toBe(1)
    expect(advanceCursor(1, token(4))).toBe(4)
    // 跳号是允许的（事务回滚会留空洞），回退不是
    expect(advanceCursor(4, token(4))).toBe(4)
  })
})

describe('streamChat 的续传游标与中止', () => {
  // 这条钉的是 issue #79 的验收："中止后不触发错误提示，且不污染 lastEventId"。
  it('中止不触发 onError，游标停在最后一条已持久化的事件上', async () => {
    const controller = new AbortController()
    mockFetchChunks([frame(1, 'token', { text: '半' }), frame(2, 'token', { text: '截' })], {
      signal: controller.signal,
      holdOpen: true,
    })

    const events: ChatEvent[] = []
    const onError = vi.fn()
    const done = streamChat(
      'c-1',
      '你好',
      'key',
      { onEvent: (e) => events.push(e), onError },
      controller.signal,
    )

    // 等两帧都收到了再中止——它测的必须是"流中断"而不是"流没开始"。
    await vi.waitFor(() => expect(events).toHaveLength(2))
    controller.abort()

    // 中止是用户自己的决定，不是生成失败：不抛、不报错，正常返回。
    await expect(done).resolves.toBe(2)
    expect(onError).not.toHaveBeenCalled()
  })

  // 【游标为什么是 2 而不是 3，也不是 null】那条 error 帧没有 id，它对应的
  // 失败发生在"分配 event_id"之前（后端 sse.go 的 writeFallbackError，整行
  // 省掉 id）。游标必须停在最后一条真实事件上，否则下次
  // GET .../events?after_event_id=N 会把 2 之后、用户还没收到的事件全跳过。
  it('没有 id 的 error 帧不推进游标', async () => {
    mockFetchChunks([
      frame(1, 'token', { text: '你' }),
      frame(2, 'token', { text: '好' }),
      frame(null, 'error', { type: 'internal_error', detail: '事件没能落库' }),
    ])

    const onError = vi.fn()
    const cursor = await streamChat('c-1', '你好', 'key', {
      onEvent: () => {},
      onError,
    })

    expect(cursor).toBe(2)
    // 这一帧仍然是"要把失败告诉用户"的通道，只是不参与发号。
    expect(onError).not.toHaveBeenCalled()
  })

  it('正常收完一条流时游标是最后一条事件的 id', async () => {
    mockFetchChunks([frame(1, 'token', { text: '你' }), frame(2, 'done', {})])

    const types: string[] = []
    const cursor = await streamChat('c-1', '你好', 'key', {
      onEvent: (e) => types.push(e.type),
      onError: () => {},
    })

    expect(types).toEqual(['token', 'done'])
    expect(cursor).toBe(2)
  })
})
