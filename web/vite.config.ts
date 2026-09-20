import path from 'node:path'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

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
