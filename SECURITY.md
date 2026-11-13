# 安全策略

## 报告问题

请不要在公开 Issue 里贴漏洞细节、API Key、数据库连接串、主密钥或私人文档内容。

优先使用 GitHub 的私密漏洞报告入口：<https://github.com/XiaoleC05/CongoRAG/security/advisories/new>

如果这个入口不可用，先开一个 Issue，只写“需要私下报告安全问题”，不要写复现细节。维护者会给你一个私下沟通的渠道。

## 范围

CongoRAG 默认只监听 `127.0.0.1`，没有登录和权限系统。把 API 直接暴露到公网属于部署配置问题，不是产品内置的安全边界。公网演示形态（`deployments/production/`）在 nginx 层加 Basic Auth + token cookie，这一层不在 Go 进程里。

报告时请说明：

- 受影响的版本或 commit
- 复现步骤
- 影响范围
- 是否已经在公开渠道披露

## 内置防护（v6.4.0 起）

| 层 | 做什么 | 在哪里 |
| --- | --- | --- |
| 监听边界 | 默认只绑回环；`OriginCheck` 校验 Host 头与 Origin 头，拒绝 DNS rebinding 与跨站请求 | `internal/platform/middleware.go` |
| 安全响应头 | `X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`Permissions-Policy`、`Cross-Origin-Opener-Policy`、CSP（`frame-ancestors 'none'`、`script-src 'self'`、`worker-src 'self' blob:`） | `internal/platform/security.go` |
| 限流 | 两档令牌桶：general 240/min（burst 60）覆盖全部 `/api/`；expensive 30/min（burst 8）覆盖 14 个会调模型/embedding 或触发大规模重索引的 POST（含 `POST /knowledge-bases/{id}/search`）。静态资源、SPA 页面与健康检查**不计数**——否则打开一次页面就会吃掉配额。超限返回 429 `rate_limited` + `Retry-After` | `internal/platform/ratelimit.go` |
| 上传 | 扩展名白名单 + **内容嗅探**（PDF 必须 `%PDF-`、文本必须无 NUL 且是合法 UTF-8）+ 文件名净化（去路径、去控制字符）+ 请求体上限（默认 32 MiB） | `internal/knowledge/upload_validation.go`、`handler_knowledge.go` |
| 流式连接 | SSE 空闲 15s 发一条 `: keep-alive` 注释帧（客户端按规范忽略），避免反代掐断长任务；所有写帧入口共用一把锁，防止心跳与事件交错 | `apps/api/internal/api/sse.go` |
| 前端请求 | 非流式请求 120s 超时（连接假死不会让界面永远转圈）；流式请求单独处理，不受影响 | `packages/api-client/index.ts` |
| 密钥 | API Key 用 AES-GCM 加密落库，主密钥首次启动生成并落盘（0600）；响应只回显"是否配置过" | `internal/platform/secretbox.go`、`internal/llm/` |
| 提示词 | 提示词注册表启动校验；审计页面只读，不提供运行时改写入口 | `internal/prompt/` |

### 限流配置

| 环境变量 | 默认 | 说明 |
| --- | ---: | --- |
| `CONGORAG_RATE_LIMIT_PER_MINUTE` | 240 | general 档每分钟令牌数；0 关闭 |
| `CONGORAG_RATE_LIMIT_BURST` | 60 | general 档桶容量 |
| `CONGORAG_RATE_LIMIT_LLM_PER_MINUTE` | 30 | expensive 档每分钟令牌数；0 关闭 |
| `CONGORAG_RATE_LIMIT_LLM_BURST` | 8 | expensive 档桶容量 |

非法值（负数、开启档位但 burst 为 0）会让进程**拒绝启动**——限流是防线，配置错了不能静默失效。

客户端识别：直连时按 `RemoteAddr`；当 `RemoteAddr` 是回环地址（生产 nginx 反代场景）时取 `X-Forwarded-For` 第一跳。**不盲信** XFF：外部请求如果伪造它，来源地址不是回环，XFF 会被忽略。

### 生产反代要求（重要）

限流的客户端识别只在"直连对端是回环"时采用 `X-Forwarded-For` 第一跳。
生产 nginx **必须覆盖式设置**：

```nginx
proxy_set_header X-Forwarded-For $remote_addr;
```

用追加式 `$proxy_add_x_forwarded_for` 时，最左值来自客户端、可被轮换伪造，
限流按伪造地址分桶等于没有限流。代码与测试固定了"直连伪造 XFF 无效"这一条
（`internal/platform/ratelimit_test.go`），但反代配置本身不在进程内。

### 已知取舍（不是缺陷，是边界）

- **CSP 的 `style-src` 允许 `'unsafe-inline'`**：landing 静态页内联了样式，Radix 组件在运行期用 style 属性定位。样式内联不构成脚本执行；脚本侧仍由 `script-src 'self'` 收紧。
- **CSP 的 `script-src` 带一个 sha256**：`web/index.html` 里有一段同步"主题引导"内联脚本（防浅色闪屏），按 `script-src 'self'` 会被拦截。这里放行的是**那一段确定的脚本哈希**，不是 `'unsafe-inline'`；脚本一改哈希就对不上（`security_test.go` 读文件现算，防漂移）。后续可把它挪成外部 `/theme-init.js` 并删掉 hash。
- **没有 CSRF token**：默认只监听回环且校验 Origin/Host，跨站请求在 `OriginCheck` 已被 403。公网形态由 nginx 的 Basic Auth + 同源 cookie 承担。
- **限流按客户端地址**：单机单用户场景够用；多用户共享部署需要按账号限流，属于 Roadmap。
- **SSE 心跳是注释帧**：`lastEventId` 不会因为心跳推进，断线续传语义不变。

## 不在范围内

- 需要先拿到本机 root/管理员权限的问题
- 用户自己把 `CONGORAG_LISTEN_ADDR` 改成 `0.0.0.0` 且没有加认证导致的暴露
- 第三方模型服务、PostgreSQL、Docker 本身的问题

## 支持版本

安全修复只保证落在当前主分支和最新发布版本上。旧版本不单独维护。
