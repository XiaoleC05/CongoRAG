import path from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
// 从 vitest/config 导入 defineConfig，而不是 vite——两者是同一个函数，
// vitest 只是把 UserConfig 的 `test` 字段补上了类型。
// 用 vite 那个版本写 test 会报"对象字面量只能指定已知属性"。
import { defineConfig } from 'vitest/config'

// 配置文件的目录（= web/）。
// 用 import.meta.dirname 而不是 __dirname：Vite 8 会对 __dirname 发告警，
// 说将来切到 native config loader 时不再注入它。
const here = import.meta.dirname

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    // Tailwind v4 走专属 Vite 插件，不用 PostCSS。
    // 所以没有 postcss.config.js、没有 autoprefixer、没有 tailwind.config.js——
    // 主题写在 src/index.css 的 @theme 块里。
    tailwindcss(),
  ],

  resolve: {
    // shadcn 生成的组件用 "@/" 引用，见 src/components/ui/*.tsx。
    // 这个别名在三处保持一致：这里、tsconfig.app.json 的 paths、components.json 的 aliases。
    alias: {
      '@': path.resolve(here, './src'),
    },
  },

  build: {
    // 产物直接输出到 go:embed 要读的目录。
    // 不配的话默认输出到 web/dist/，之后还得搬到 apps/api/web/。
    outDir: '../apps/api/web',

    // outDir 在项目根目录之外时 Vite 默认不清空它，显式打开，
    // 保证每次构建都是干净的一份，不残留上一次的文件。
    emptyOutDir: true,

    // 第三方库拆成独立的 chunk。
    //
    // 【拆的是 rolldown 的 codeSplitting，不是 rollup 时代的 manualChunks】
    // Vite 8 底层是 rolldown。它的类型定义里 output.manualChunks 和
    // output.advancedChunks 都标了 @deprecated，而且写着
    // "If `advancedChunks` and `codeSplitting` are both specified,
    //  `advancedChunks` option will be ignored"。只配 manualChunks 会得到一份
    // 不报错、也不生效的配置：构建照样成功，产物还是老样子，没有任何提示。
    //
    // 【正则里的路径分隔符必须写 [\\/]，不能写 /】
    // rolldown 的 CodeSplittingGroup 文档原话："it's recommended to use
    // `[\\/]` to match the path separator instead of `/` to avoid potential
    // issues on Windows"。模块 id 在 Windows 上是反斜杠路径，
    // /node_modules/react/ 这条正则在 Windows 上永远匹配不上——同样是静默失效。
    // 这是本项目的主力开发平台，所以每条 test 都得带 [\\/]。
    //
    // 【它换来了什么，没换来什么】别把"入口 chunk 变小"当成"首屏变小"。
    // 这些库是入口的静态依赖，Vite 会给它们在 index.html 里写 modulepreload，
    // 浏览器照样要在首次渲染前把它们全下完——只是能并行下。
    // 实测（未压缩）：单入口 524,354 B → 入口 41,682 B + 6 个 modulepreload
    // 共 505,288 B，首屏字节只降了 3.6%，而且降的全是路由级懒加载搬走的页面代码。
    // 这个配置真正的收益是缓存粒度：业务代码每次发布都变，
    // 第三方库不变就能一直命中浏览器缓存。
    rolldownOptions: {
      output: {
        codeSplitting: {
          // 【tags: ['$initial'] 不是可有可无的装饰，少了它首屏反而变大】
          // 一个组默认会把匹配到的模块【全】收进同一个 chunk，而这个 chunk 是
          // 入口的静态依赖 → 会被 modulepreload 拉上首屏。于是只被懒加载页面
          // 用到的库也跟着上来了：ScrollArea（只有会话页用）、hover-card
          // （只有引用气泡用）、各种只出现在详情页的 lucide 图标，
          // 本来被自动分块留在懒加载 chunk 里，一配置分组就被拽回首屏。
          // 实测：不加这条，首屏 505,288 B → 528,702 B，比不加分组还大 22 KB。
          //
          // '$initial' 是 rolldown 的内置标签，含义是"被入口静态导入、或位于
          // 它的依赖链上"。加上它，分组只挑真正在首屏路径上的模块，
          // 懒加载专属的那部分回落到自动分块，继续留在懒加载 chunk 里。
          //
          // priority 大的组先挑模块，被挑走的模块会从后面的组里移除。
          groups: [
            // react / react-dom / scheduler 放同一组：它们互相依赖，
            // 再往下拆只会制造循环 chunk。
            {
              name: 'react',
              test: /[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/,
              priority: 30,
              tags: ['$initial'],
            },
            {
              name: 'react-router',
              test: /[\\/]node_modules[\\/]react-router[\\/]/,
              priority: 25,
              tags: ['$initial'],
            },
            {
              name: 'tanstack',
              test: /[\\/]node_modules[\\/]@tanstack[\\/]/,
              priority: 20,
              tags: ['$initial'],
            },
            // 同时匹配 radix-ui（聚合包）和 @radix-ui/*（单包），
            // shadcn 生成的组件走的是前者的 re-export。
            {
              name: 'radix',
              test: /[\\/]node_modules[\\/](@radix-ui|radix-ui)[\\/]/,
              priority: 15,
              tags: ['$initial'],
            },
            // 兜底：其余第三方库合并成一个。minSize 让攒不够的组退回自动分块——
            // 为几 KB 单开一个请求，多出来的往返比省下的字节更贵。
            {
              name: 'vendor',
              test: /[\\/]node_modules[\\/]/,
              priority: 1,
              tags: ['$initial'],
              minSize: 20000,
            },
          ],
        },
      },
    },
  },

  test: {
    // 【不配 globals】保持每个测试文件自己 import describe/it/expect 的写法，
    // 和已有的 7 个测试文件一致。副作用是 @testing-library 的自动清理不生效
    // （它靠全局的 afterEach 判断），所以每个渲染的测试文件要自己 afterEach(cleanup)。
    //
    // 【不配 environment】三个环境需求不同的文件各自在文件头写
    // `// @vitest-environment jsdom`，别在这里一刀切。
    //
    // 这里只做一件事：把 jsdom 缺的浏览器 API 补上（见 src/test/setup.ts）。
    setupFiles: ['./src/test/setup.ts'],
  },

  server: {
    // 开发代理：前端请求 /api/... 时转发到 Go 服务。
    //
    // 开发期页面在 :5173、API 在 :3210，属于不同的源，浏览器会按跨域拦下来。
    // 代理之后浏览器看到的始终是同一个源，不触发跨域检查；
    // 前端代码里写的是相对路径，交付期（前端被 Go 内嵌、同源）不用改。
    //
    // 交付期这两条前缀由 apps/api 的 spa.go 排除，两边要保持一致。
    proxy: {
      '/api': 'http://127.0.0.1:3210',
      '/healthz': 'http://127.0.0.1:3210',
    },
  },
})
