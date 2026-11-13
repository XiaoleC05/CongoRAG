import {
  expect,
  expectLoaded,
  expectNoHorizontalOverflow,
  IDS,
  mockApi,
  test,
} from './fixtures'

/**
 * 逐页 smoke：每页都断言 h1、无 console error/pageerror、1440×900 无横向溢出，
 * 再对三个典型页面在 390×844 下补"控件真的能点、点完有可见结果"。
 *
 * 【为什么每页都要量溢出】R1 审计发现过"六列表格被 overflow-hidden 裁掉、
 * 而 documentElement.scrollWidth 看不出来"的静默缺陷；横向溢出是这一页
 * 最容易被新增列/长文案破坏的性质，逐页量一次成本很低。
 */

const DESKTOP = { width: 1440, height: 900 }
const MOBILE = { width: 390, height: 844 }

type PageCase = {
  name: string
  path: string
  /** 页面 h1 的文本；会话详情页按设计没有 h1（见 headingStructure.test.ts 例外清单）。 */
  heading?: string
  /** 数据真的渲染出来了的证据（防止在骨架态就量溢出、断言为绿）。 */
  readyText?: string
}

const DESKTOP_PAGES: PageCase[] = [
  { name: '知识库列表', path: '/knowledge-bases', heading: '知识库', readyText: 'Transformer 论文库' },
  { name: '对话列表', path: '/conversations', heading: '对话', readyText: '关于注意力机制的讨论' },
  { name: 'Agent 列表', path: '/agents', heading: 'Agent', readyText: '算数助手' },
  { name: '用量', path: '/usage', heading: '用量', readyText: '调用次数' },
  { name: 'Prompt 审计', path: '/prompts', heading: 'Prompt 审计', readyText: '对话系统提示词' },
  { name: '设置', path: '/settings', heading: '设置', readyText: 'test-chat-model' },
  { name: '会话详情', path: `/conversations/${IDS.conversation}`, readyText: '注意力机制让模型' },
  { name: 'Agent 详情', path: `/agents/${IDS.agent}`, heading: '算数助手' },
  {
    name: '执行轨迹',
    path: `/agents/${IDS.agent}/runs/${IDS.run}`,
    heading: '执行轨迹',
    readyText: 'calculator',
  },
]

test.describe('桌面 smoke（1440×900，全 mock API）', () => {
  for (const pageCase of DESKTOP_PAGES) {
    test(`${pageCase.name}：h1 可见、无报错、无横向溢出`, async ({ page, consoleErrors }) => {
      await page.setViewportSize(DESKTOP)
      const { unmatched } = await mockApi(page)
      await page.goto(pageCase.path)

      if (pageCase.heading) {
        await expect(
          page.getByRole('heading', { level: 1, name: pageCase.heading }),
          `${pageCase.name}：h1 不可见`,
        ).toBeVisible()
      } else {
        // 会话详情页是 headingStructure.test.ts 里登记的例外：页面主体是一列消息，
        // 没有"这一页叫什么"的信息。这里改用"输入框可见"作为页面就绪的证据。
        await expect(page.getByPlaceholder('输入消息…')).toBeVisible()
      }

      if (pageCase.readyText) {
        await expect(
          page.getByText(pageCase.readyText).first(),
          `${pageCase.name}：fixture 数据没有渲染出来`,
        ).toBeVisible()
      }
      // 等所有骨架消失：此刻要么数据到了、要么错误态出来了，量溢出才有意义。
      await expectLoaded(page)
      await expectNoHorizontalOverflow(page, pageCase.name)

      expect(
        consoleErrors,
        `${pageCase.name}：出现 console error/pageerror\n${consoleErrors.join('\n')}`,
      ).toEqual([])
      expect(unmatched, `${pageCase.name}：有未 mock 的 API -> ${unmatched.join(', ')}`).toEqual([])
    })
  }
})

test.describe('窄屏 smoke（390×844）', () => {
  test('知识库列表：无横向溢出，"创建"能打开弹窗', async ({ page, consoleErrors }) => {
    await page.setViewportSize(MOBILE)
    const { unmatched } = await mockApi(page)
    await page.goto('/knowledge-bases')
    await expect(page.getByRole('heading', { level: 1, name: '知识库' })).toBeVisible()
    await expectLoaded(page)
    await expectNoHorizontalOverflow(page, '知识库列表（窄屏）')

    await page.getByRole('button', { name: '创建' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)

    expect(consoleErrors, consoleErrors.join('\n')).toEqual([])
    expect(unmatched, unmatched.join(', ')).toEqual([])
  })

  test('Prompt 审计：无横向溢出，版本切换在窄屏可用', async ({ page, consoleErrors }) => {
    await page.setViewportSize(MOBILE)
    const { unmatched } = await mockApi(page)
    await page.goto('/prompts')
    await expect(page.getByRole('heading', { level: 1, name: 'Prompt 审计' })).toBeVisible()
    await expect(page.getByText('对话系统提示词').first()).toBeVisible()
    await expectNoHorizontalOverflow(page, 'Prompt 审计（窄屏）')

    // 默认是 activeVersion v2，切到 v1 后内容必须真的换掉。
    await expect(page.getByText(/每个事实句末尾必须用/)).toBeVisible()
    await page.getByRole('button', { name: 'v1' }).click()
    await expect(page.getByText(/旧版：只依据检索片段回答/)).toBeVisible()
    await expect(page.getByText(/每个事实句末尾必须用/)).toHaveCount(0)
    await expectNoHorizontalOverflow(page, 'Prompt 审计（窄屏，切换版本后）')

    expect(consoleErrors, consoleErrors.join('\n')).toEqual([])
    expect(unmatched, unmatched.join(', ')).toEqual([])
  })

  test('阅读器：PDF 加载、无横向溢出、目录/翻译抽屉可开可关', async ({ page, consoleErrors }) => {
    await page.setViewportSize(MOBILE)
    const { unmatched } = await mockApi(page)
    await page.goto(`/documents/${IDS.doc}/reader?page=1`)

    // h1 是 sr-only（视觉上不占位），所以断言 attached 而不是 visible。
    await expect(
      page.getByRole('heading', { level: 1, name: /论文阅读器：sample\.pdf/ }),
    ).toBeAttached()
    // canvas 的 aria-label 里带"共 N 页"——N 只有 pdf.js 真的解析完文档才知道。
    // 不用"下一页是否可用"当信号：这份 fixture 只有 1 页，下一页本来就该禁用。
    await expect(page.locator('canvas')).toHaveAttribute('aria-label', /PDF 第 1 页，共 1 页/)
    await expectNoHorizontalOverflow(page, '阅读器（窄屏）')

    // 窄屏「目录」抽屉：按钮可见 → 点开有可见结果。
    await page.getByRole('button', { name: '目录' }).click()
    await expect(page.getByRole('dialog').getByRole('heading', { name: '章节目录' })).toBeVisible()
    // 必须限定在 dialog 里：桌面端的常驻目录 aside 也在 DOM 里（只是 hidden），
    // 不限定的话 getByText 会同时命中两处，strict mode 直接报错。
    await expect(page.getByRole('dialog').getByText('1 Introduction')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)

    // 窄屏「双语对照」抽屉：这是 R4 修的"假按钮"——点了必须有抽屉。
    await page.getByRole('button', { name: '双语对照' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expect(
      page.getByRole('dialog').getByText('原文（选中文字可划词翻译）'),
    ).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)

    expect(consoleErrors, consoleErrors.join('\n')).toEqual([])
    expect(unmatched, unmatched.join(', ')).toEqual([])
  })
})
