// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'

import { errorMessage, errorPresentation } from '@/lib/errors'
import { writeClipboard } from '@/lib/clipboard'

/** 见 CopyButton.test.tsx 的同名说明：jsdom 没有 clipboard，得自己定义。 */
function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

afterEach(() => {
  setClipboard(undefined)
})

/** 抓住 writeClipboard 抛出来的东西，顺便做一次类型收敛。 */
async function failure(text = 'x'): Promise<unknown> {
  try {
    await writeClipboard(text)
  } catch (err) {
    return err
  }
  throw new Error('本该抛出，却成功了')
}

describe('writeClipboard', () => {
  it('成功时 resolve，且调用的是 writeText(text)', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })

    await expect(writeClipboard('hello')).resolves.toBeUndefined()
    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('没有 navigator.clipboard 时抛出，并且能被人读懂', async () => {
    setClipboard(undefined)

    const err = await failure()
    // 【为什么必须显式判断而不是靠 try/catch】非安全上下文下这个属性是
    // undefined，直接调会抛 TypeError: Cannot read properties of undefined
    // ——用户看到的是一句和剪贴板毫无关系的报错。
    expect(errorMessage(err)).toContain('剪贴板')
    expect(errorMessage(err)).toContain('HTTPS')
  })

  it('writeText 被拒时抛出，文案里带下一步怎么做', async () => {
    setClipboard({
      writeText: vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError')),
    })

    const err = await failure()
    expect(errorMessage(err)).toContain('权限')
  })

  it('失败走 mutation 语境的 toast，而不是留在页面上', () => {
    // 判据不在组件里（README §16），这里钉的是"这一类的归宿"：
    // 复制失败不改变页面还剩什么内容，重试一次就行。
    expect(errorPresentation({ type: 'clipboard_denied' }, 'mutation')).toBe('toast')
  })
})
