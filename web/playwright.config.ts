import { defineConfig, devices } from '@playwright/test'

/**
 * 浏览器端到端测试（R5）。
 *
 * 【为什么它是独立的测试层，而不是塞进 vitest】jsdom 跑不了真实浏览器：
 * 滚动溢出、focus 转移、Radix 的 portal/动画、PDF canvas 这些都不存在。
 * R4 的截图脚本已经证明"真浏览器 + 全 mock 接口"能覆盖这些，这里把它
 * 固化成可复跑的测试，并接进 CI。
 *
 * 【不依赖 api / 数据库 / 模型 Key】所有 /api/v1/** 请求都由
 * e2e/fixtures.ts 的 page.route 拦截并返回契约形状的 fixture；
 * 外部域名一律 abort（见 fixtures 里的注释）。所以 CI 不需要 Postgres、
 * 不需要任何 Key，测试也不花钱。
 *
 * 【workers: 1】只有一个 webServer（:5188），多 worker 会并行复用同一实例；
 * 单 worker 换来的是"失败可复现"，而这套用例总共几十秒，不值得并行。
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  // 只在 CI 重试：trace 是 on-first-retry，本地不重试就不会有"第一次失败被重试掩盖"。
  retries: process.env.CI ? 1 : 0,
  forbidOnly: !!process.env.CI,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: 'list',
  outputDir: 'test-results',
  use: {
    baseURL: 'http://localhost:5188',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    // 用 dev server 而不是构建产物：E2E 要覆盖的是"开发时就能跑"的路径，
    // 且不用等 tsc -b；生产构建由 CI 的 web job 单独兜住。
    command: 'pnpm exec vite --port 5188 --strictPort',
    url: 'http://localhost:5188',
    // 本机已经起着 dev server 时直接复用，避免"端口被占用"导致本地跑不了。
    reuseExistingServer: true,
    timeout: 60_000,
  },
})
