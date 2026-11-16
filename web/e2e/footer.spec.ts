import { expect, expectLoaded, mockApi, test } from './fixtures'

/**
 * 备案信息回归（合规要求，改页脚时最先坏、坏了却没人发现的那类东西）。
 *
 * 【为什么 E2E 而不是单测】备案号是渲染在 AppLayout 页脚里的：单测要挂一整个
 * 路由树才能看到它；而"图标真的能加载（naturalWidth > 0）"这种性质只有真浏览器
 * 才量得出来。图标文件缺失时 <img> 依然在 DOM 里，文本断言会绿——所以必须查
 * naturalWidth。
 */
test.describe('页面底部备案信息', () => {
  test('ICP 与公安网备同时可见，链接/图标正确', async ({ page, consoleErrors }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const { unmatched } = await mockApi(page)
    await page.goto('/knowledge-bases')
    await expect(page.getByRole('heading', { level: 1, name: '知识库' })).toBeVisible()
    await expectLoaded(page)

    const footer = page.locator('footer')
    await expect(footer.getByText('鲁ICP备2026038838号-2')).toBeVisible()

    const police = footer.getByRole('link', { name: /鲁公网安备37028202001377号/ })
    await expect(police).toHaveAttribute(
      'href',
      'https://beian.mps.gov.cn/#/query/webSearch?code=37028202001377',
    )
    await expect(police).toHaveAttribute('target', '_blank')
    await expect(police).toHaveAttribute('rel', 'noreferrer noopener')

    const icon = police.locator('img')
    await expect(icon).toHaveAttribute('src', '/police-beian.png')
    await expect(icon).toHaveAttribute('alt', '公安备案图标')
    // 图标 404 时 img 仍在 DOM 里，只有 naturalWidth 能证明它真的被加载了。
    await expect
      .poll(() => icon.evaluate((el) => (el as HTMLImageElement).naturalWidth))
      .toBeGreaterThan(0)

    await page.screenshot({ path: 'test-results/footer-app.png' })

    expect(consoleErrors, consoleErrors.join('\n')).toEqual([])
    expect(unmatched, unmatched.join(', ')).toEqual([])
  })
})
