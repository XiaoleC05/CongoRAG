# SSE 并发压测报告（R5）

> 目标：给评审一套**可复现**的压测证据，而不是"我们测过，很快"。
> 本报告中的所有数字都来自 2026-11-14 本机真实运行，命令与版本见 §7。

## 1. 结论摘要

三档并发（25 / 50 / 100 条长连接，每档 10 秒，mock SSE 目标）全部 **100% 连接成功、
0 错误**。吞吐随并发近似线性增长（465 → 952 → 1658 events/s），100 并发时 TTFB
p50 从 ~13ms 跳到 ~23ms——这是**压测器自身单进程事件循环**的拐点，不是服务端拐点
（mock 服务与压测客户端在同一个 Node 进程里，详见 §6）。

| 并发连接 | 成功率 | TTFB p50 | TTFB p95 | TTFB p99 | events/s | 接收字节/s | 错误数 | RSS 峰值 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 25 | 100.0% | 12.6 ms | 13.5 ms | 14.8 ms | 465.0 | 36.2 KB/s | 0 | 56.7 MB |
| 50 | 100.0% | 12.9 ms | 13.1 ms | 13.2 ms | 952.3 | 74.2 KB/s | 0 | 57.8 MB |
| 100 | 100.0% | 23.5 ms | 24.2 ms | 24.3 ms | 1658.4 | 129.0 KB/s | 0 | 63.1 MB |

## 2. 环境

| 项 | 值 |
| --- | --- |
| 机器 | Intel Core i7-14700KF（20 核 / 28 逻辑核），31.8 GB RAM |
| 系统 | Windows 11 家庭版（build 26300） |
| Node | v22.19.0（压测器与 mock 服务同一进程） |
| Go | go1.26.4（§4 的 SSE benchmark 用） |
| 测量时负载 | 同机还有前端构建/测试进程在跑（并行开发），所以 TTFB 绝对值偏保守；三档成功率与吞吐仍稳定 |

## 3. 方法论

**被测对象**：mock SSE 服务（内置在 `ops/loadtest/stream-load.mjs`）按
[docs/sse-protocol.md](sse-protocol.md) 的帧格式发送：
`id: N\nevent: token\ndata: {"type":"token","data":{"text":"..."}}\n\n`，
每连接默认 50ms 一个 token 帧（理论 20 events/s/连接）。

**客户端**：与前端 `web/src/lib/streamChat.ts` 同一套分帧逻辑——累积字节、
按空行切帧、逐行解析 `id:`/`event:`/`data:`、忽略 `:` 注释帧。所以它压的
是"客户端解析路径 + 回环网络"，不是 EventSource 内部实现。

**测量口径**：

- 连接成功率 = 收到至少一帧且 HTTP < 400 的连接数 / 请求连接数；
- TTFB = 从发起请求到**解析出第一条事件**的时间（不是首字节，含分帧与解析）；
- events/s = 所有连接在测量窗口内收到的事件总数 / 实际耗时；
- 接收字节/s = 客户端 `data` 回调收到的 payload 字节总和 / 实际耗时（不含 HTTP 头）；
- RSS 峰值 = 压测进程每 100ms 采一次 `process.memoryUsage().rss` 的最大值；
- 每档跑满 `duration` 秒后主动断开连接，避免服务端无限写。

**档位**：`--concurrency 25,50,100 --duration 10 --interval 50`，三档串行跑，
每档 10 秒，总计约 30 秒。

## 4. Go 侧 SSE 写帧 benchmark（`apps/api/internal/api/sse_bench_test.go`）

压测模拟量的是端到端；Go benchmark 量的是**服务端写一帧的常数**。两者互补：
如果 SSE 帧格式或 `sseSink` 的锁被改坏，压测的 TTFB 不一定看得出，但 benchmark 会。

| 基准 | ns/op | B/op | allocs/op | 说明 |
| --- | ---: | ---: | ---: | --- |
| `BenchmarkSSEEmit` | 161.7 | 64 | 4 | 单帧：拼 id/event/data + 拿锁 + 写丢弃型 writer |
| `BenchmarkSSESinkConcurrent/workers=8` | 195.0 | 64 | 4 | 8 goroutine 同时 Emit |
| `BenchmarkSSESinkConcurrent/workers=32` | 215.6 | 64 | 4 | 32 goroutine 同时 Emit |

读法：并发 8/32 时每帧成本比单帧高 **20.7% / 33.4%**——这是 `sseSink.mu`
（心跳 goroutine 与 handler 共用的写锁，见 `sse.go`）的竞争量级。
分配在三种情况下完全相同（64 B / 4 allocs），说明并发路径没有额外分配；
写锁是唯一成本来源。如果将来要提升单连接吞吐，先看这里的数再决定是否分片写。

命令：

```bash
go test -run '^$' -bench 'BenchmarkSSE' -benchmem ./apps/api/internal/api
```

## 5. 用真实后端跑（`--target URL`）

mock 模式的价值是"离线、可复现、零依赖"；要评估真实链路的 SSE 承载能力，把
`--target` 指向正在跑的 api 即可。鉴权头可选（`--header` 可重复）：

```bash
# 真实聊天流（POST；conversation id 换成你自己的；Bearer token 按需）
node ops/loadtest/stream-load.mjs \
  --target http://127.0.0.1:3210/api/v1/conversations/<conversation-id>/messages \
  --method POST --body '{"text":"你好"}' \
  --header "Authorization: Bearer <token>" \
  --concurrency 10,25 --duration 15
```

本机已用一条临时独立 SSE 服务（127.0.0.1:45999，20ms/帧）验证过 `--target URL` 路径：
5 条连接 × 2 秒，100% 成功、0 错误、TTFB p50 28.9ms——说明 URL 模式确实在独立服务上
工作，而不是只在 mock 模式里自洽。

注意事项：

- 真实后端需要 PostgreSQL 与可用的 chat 模型（BYOK）；**没有这两样时这条命令只会
  得到 4xx/5xx 或空流**，请勿把失败误读为"系统不行"。
- 真实链路的延迟主要由检索 SQL 与模型首字决定，量级通常是秒级；把本报告 §1 的
  ~13ms TTFB 与真实链路对比没有意义——mock 数字只说明**压测器与客户端解析路径**
  的容量。
- Agent 运行流用 `GET /api/v1/runs/{runId}/events?after_event_id=0`，也可以把
  `--target` 指向它（GET 模式），但 run 的生成速度由模型决定。

## 6. 局限（不夸大）

1. **mock 服务与压测客户端在同一个 Node 进程**：100 并发时两边共享一个事件循环，
   TTFB p50 跳到 23ms、events/s 只达到理论值（2000）的 83%，拐点在压测器这一侧。
   要测更大并发，应该把 mock 服务拆成独立进程（或直接 `--target` 真实服务）。
2. **回环网络**没有真实网络的 RTT/丢包/带宽限制；不能拿它评估"公网用户"体验。
3. **单机 Windows + 共享开发机负载**：绝对值受并行构建影响，跨机器比较请重跑。
4. 它**不替代**数据库/模型链路的压测；那部分需要真实服务，命令见 §5。

## 7. 复现

```bash
# 三档并发，JSON + Markdown 片段输出到指定路径（两个参数都可选）
node ops/loadtest/stream-load.mjs --concurrency 25,50,100 --duration 10 --interval 50 \
  --json ops/loadtest/results/stream-$(date +%Y%m%d-%H%M%S).json \
  --markdown /tmp/loadtest.md

# 只跑一档、看控制台表
node ops/loadtest/stream-load.mjs --concurrency 50 --duration 5
```

- 零第三方依赖：只用 `node:http` / `node:os` / `node:fs`；
- 退出码：0 全部成功、1 有连接失败（报告照写）、2 参数/环境错误；
- `ops/loadtest/results/` 已在 `.gitignore`（每次的原始 JSON 是过程产物，结论沉淀到
  本文件；需要留存时自行附加到 PR 描述）。
