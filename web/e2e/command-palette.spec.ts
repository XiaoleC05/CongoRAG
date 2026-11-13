import { expect, mockApi, test } from './fixtures'

/**
 * 全局命令面板的端到端行为。
 *
 * 【为什么单独一个文件】它跨页面（在知识库页打开、导航到设置页），
 * 不适合塞进逐页 smoke；而它又是 R4 新增的核心交互，值得单独守。
 */
test.describe('命令面板（Ctrl+K）', () => {
  test('打开 → 过滤 → Enter 导航 → 再打开后 Esc 关闭', async ({ page, consoleErrors }) => {
    const { unmatched } = await mockApi(page)
    await page.goto('/knowledge-bases')
    await expect(page.getByRole('heading', { level: 1, name: '知识库' })).toBeVisible()

    // 打开
    await page.keyboard.press('Control+k')
    await expect(page.getByRole('dialog')).toBeVisible()
    const input = page.getByRole('combobox', { name: '搜索命令' })
    await expect(input).toBeFocused()

    // 过滤：只剩「设置」这一条
    await input.fill('设置')
    await expect(page.getByRole('option', { name: /设置/ })).toBeVisible()
    await expect(page.getByRole('option', { name: /知识库/ })).toHaveCount(0)

    // Enter 执行唯一命中项
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/\/settings$/)
    await expect(page.getByRole('heading', { level: 1, name: '设置' })).toBeVisible()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    // 再打开、Esc 关闭
    await page.keyboard.press('Control+k')
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)

    expect(consoleErrors, consoleErrors.join('\n')).toEqual([])
    expect(unmatched, unmatched.join(', ')).toEqual([])
  })
})
