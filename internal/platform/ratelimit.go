package platform

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 限流的几个固定值。
const (
	// problemTypeRateLimited 是 429 的 Problem.type。
	//
	// 契约里 Problem.type 是自由字符串（不是枚举），所以新增一个值不需要
	// 改生成物；约束在消费侧：前端按 type 分支的代码必须有 default 兜底，
	// 否则 429 会落进一个它不认识的分支。前端这轮由 lead 处理。
	problemTypeRateLimited = "rate_limited"

	rateLimitedTitle = "请求过于频繁"

	// bucketTTL 是"多久没出现就回收这个 client 的桶"。
	//
	// 回收一个空闲桶的代价只是下次出现时重新从满桶开始；而 bucket 本身
	// 是 map 里的一条记录，长期不清理就意味着内存随访问来源数量无界增长
	// （扫描器/脚本可以轻易制造几十万个来源）。TTL 取 10 分钟：远大于
	// 一次正常浏览会话的间隔，又让空闲来源不会留到第二天。
	bucketTTL = 10 * time.Minute

	// bucketSweepInterval 是两个全表扫描之间的最小间隔。
	// 每个请求都扫全表是 O(桶数) 的隐形开销，而空闲桶不需要分钟级精度。
	bucketSweepInterval = 1 * time.Minute

	// bucketSweepThreshold 是"桶数达到这个量必须扫一次"的硬阈值：扫描
	// 间隔没到、但来源数已经暴涨（被扫描器刷）时先扫一次把内存按住。
	bucketSweepThreshold = 4096
)

// RateLimitConfig 是两档令牌桶的启动参数。
//
// 单位统一是"每分钟令牌数"（per minute），burst 是桶容量（瞬时允许的
// 并发/突发请求数）。换算成令牌速率时除以 60，所以 perMinute=240 等价于
// 每秒 4 个令牌。
//
// 语义：
//   - perMinute == 0：这一档关闭（burst 不再有意义，传什么都忽略）；
//   - perMinute > 0 且 burst >= 1：启用，桶初始是满的（burst 个令牌）；
//   - 任何负数、或启用时 burst < 1：启动报错。宁可起不来，也不要带一个
//     "看起来配了但其实永远拒绝"或"burst 为 0 等于全拒"的配置跑起来。
type RateLimitConfig struct {
	// GeneralPerMinute/GeneralBurst 是所有 /api/ 请求的兜底档。
	GeneralPerMinute int
	GeneralBurst     int

	// LLMPerMinute/LLMBurst 是 expensive 档：会调模型/embedding/外部下载
	// 的端点走这一档。它比 general 严格得多，因为一次请求的真实成本
	// （钱、上游配额、本机 CPU）高几个数量级。
	LLMPerMinute int
	LLMBurst     int
}

// RateLimiter 是两档令牌桶的持有者。
//
// 【两档是独立的桶，不是同一桶的两把尺】expensive 请求只看 expensive 桶，
// 不额外消耗 general 令牌——否则一次对话会同时扣两档，limits 之间会
// 互相污染，"general 240、expensive 30"就不再是各自承诺的数字。
//
// 【为什么 general 档关闭时 expensive 请求回落 general】两档都关=不限流；
// 只开 general 时，expensive 端点仍然应该受 general 保护——那是它唯一
// 剩下的防线。反过来 general 开着、expensive 关着，是最容易出现的配置
// （只调 LLM 额度），这时昂贵端点不该变成完全裸奔。
type RateLimiter struct {
	general   *tokenBuckets
	expensive *tokenBuckets
}

// NewRateLimiter 按配置构造限流器；配置非法时返回错误，由装配根拒绝启动。
func NewRateLimiter(cfg RateLimitConfig) (*RateLimiter, error) {
	return newRateLimiter(cfg, time.Now)
}

// newRateLimiter 是带时钟注入的内部构造：测试用假时钟验证"等待后恢复"
// 与清理逻辑，不需要真的 sleep 一分钟。
func newRateLimiter(cfg RateLimitConfig, now func() time.Time) (*RateLimiter, error) {
	general, err := newTokenBuckets("general", cfg.GeneralPerMinute, cfg.GeneralBurst, now)
	if err != nil {
		return nil, err
	}
	expensive, err := newTokenBuckets("expensive", cfg.LLMPerMinute, cfg.LLMBurst, now)
	if err != nil {
		return nil, err
	}
	return &RateLimiter{general: general, expensive: expensive}, nil
}

// RateLimit 返回限流中间件；rl 为 nil（两档都关闭）时是纯透传。
//
// 【只限 /api/ 前缀】页面资源（/assets/*.js、index.html）与前端路由的
// 请求不算：一次页面加载会并发取几十个静态文件，而 /readyz 这类探针
// 每几秒一次——把它们算进 general 会让"打开页面"本身就把额度打满，
// 真正的防线（接口滥用）反而失去意义。静态资源的成本是磁盘读，
// 本地单机场景没有保护价值。
//
// 【为什么按 client key 而不是全局】本地单机应用的使用者通常就一个，
// 全局桶会让"浏览器开两个标签页"互相挤占；按来源分桶才能表达"谁在被
// 限制"。来源识别见 clientKey 的注释。
//
// 【Retry-After 与 detail 里的秒数取同一份计算】客户端按 header 退避时
// 不需要再解析正文；正文里的秒数是给人看的（curl / 日志），两处必须
// 一致，否则按 header 重试的人永远等不到恢复。
func RateLimit(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rl == nil || !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		buckets := rl.general
		if isExpensiveRequest(c.Request.Method, c.FullPath()) && rl.expensive != nil {
			buckets = rl.expensive
		}
		if buckets == nil {
			// 两档都关闭（配置全为 0）：/api/ 也不限流。
			c.Next()
			return
		}

		allowed, retryAfter := buckets.allow(clientKey(c.Request))
		if allowed {
			c.Next()
			return
		}

		// Retry-After 是整秒（RFC 9110 的 delta-seconds 形式）；
		// 令牌桶算出来的等待通常带小数，向上取整——向下取整会让客户端
		// 在恢复前一瞬间重试、再吃一次 429。
		seconds := int(math.Ceil(retryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		abortWithProblem(c, Problem{
			Type:   problemTypeRateLimited,
			Status: http.StatusTooManyRequests,
			Title:  rateLimitedTitle,
			Detail: fmt.Sprintf("请求过于频繁，请在 %d 秒后重试", seconds),
		})
	}
}

// expensiveRoutes 是会调用模型/embedding 或触发大规模外部工作的 POST 端点。
//
// key 是 method + gin 的路由模板（与 generated.go 的 RegisterHandlers 逐字
// 对应，含 :id / :runId 这类参数名）。用模板而不是实际 URL 是因为参数值
// 不该影响"这是不是昂贵端点"的判定。
//
// 【清单怎么来的】判据是"一次请求最终会不会花掉模型/embedding 调用，
// 或者把大量后台工作排进队列"：
//   - 对话/Agent：SSE 里跑完整轮生成（一次 run 最多 20 轮模型调用）；
//   - translate / visualize / writing / compare / citation-survey：
//     直接调 LLM；
//   - search：每次先做一次 embedding，再做两路召回（清单外补入：它同样
//     调上游，不限制的话 embedding 额度可以被一个循环脚本刷光）；
//   - arxiv-ingest：服务端下载 PDF 并走完整上传/解析流水线；
//   - documents/knowledge-bases 的 reindex：把整个库重新排进队列，
//     后续会打满 worker 与模型额度；
//   - providers：新建 provider 可能触发全库重新 embedding。
//
// 【不包含什么】普通 CRUD、上传（有字节上限）、反馈、取消：它们要么是
// 本地 SQL 写，要么已有别的上限，混进来会把"昂贵"这个词稀释掉。
var expensiveRoutes = map[string]struct{}{
	"/api/v1/conversations/:id/messages":          {},
	"/api/v1/agents/:id/runs":                     {},
	"/api/v1/runs/:runId/resume":                  {},
	"/api/v1/translate":                           {},
	"/api/v1/visualize":                           {},
	"/api/v1/knowledge-bases/:id/writing":         {},
	"/api/v1/knowledge-bases/:id/compare":         {},
	"/api/v1/knowledge-bases/:id/citation-survey": {},
	"/api/v1/knowledge-bases/:id/arxiv-ingest":    {},
	"/api/v1/knowledge-bases/:id/search":          {},
	"/api/v1/documents/:id/version-diff":          {},
	"/api/v1/documents/:id/reindex":               {},
	"/api/v1/knowledge-bases/:id/reindex":         {},
	"/api/v1/providers":                           {},
}

// isExpensiveRequest 判断一个已经匹配到路由的请求是否走 expensive 档。
//
// 【为什么要看 FullPath 而不是 URL.Path】实际 URL 里 id 是具体的 uuid，
// 模板匹配表不能按实际路径查；gin 在进入中间件之前已经完成路由匹配，
// c.FullPath() 拿到的就是路由模板。对 404（NoRoute）它返回空串，落回
// general 档——不存在的端点没有"昂贵"属性。
func isExpensiveRequest(method, fullPath string) bool {
	if method != http.MethodPost {
		return false
	}
	_, ok := expensiveRoutes[fullPath]
	return ok
}

// clientKey 提取限流的分桶键。
//
// 【为什么不能无脑信 X-Forwarded-For】它是请求头，任何客户端都能自己写；
// 如果无条件采用，攻击者每次换一个 XFF 值就得到无限个新桶，限流形同虚设。
// 只有"直连对端是回环地址"时才可能是本机的反向代理（生产 nginx 反代
// 场景），这时才看 XFF 的第一跳。
//
// 【第一跳指的是最左边那个】按 RFC 7239/事实标准，代理把原始客户端 IP
// 追加在最左边？——不，事实标准是追加在右边、原始客户端在最左边。
// 因此 nginx 必须用覆盖式配置（proxy_set_header X-Forwarded-For
// $remote_addr）而不是追加式（$proxy_add_x_forwarded_for）：追加式下
// 最左边是客户端自己可以伪造的值，攻击者轮换伪造值即可绕过限流。
// 这条部署要求由 R6 的部署文档承接。
//
// 【非回环对端一律用 RemoteAddr】直连的客户端伪造 XFF 也没有意义，
// 因为我们根本不看它。
func clientKey(r *http.Request) string {
	host := remoteHost(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if candidate := strings.TrimSpace(first); candidate != "" {
				return candidate
			}
		}
	}
	if host == "" {
		// RemoteAddr 缺失（进程内测试、自定义 Transport）时给一个稳定键，
		// 而不是空串：空串会让所有无来源请求共用一个桶，行为不可预测。
		return "unknown"
	}
	return host
}

// remoteHost 从 RemoteAddr（host:port 或纯 host）里取出 host。
//
// IPv6 字面量带方括号（[::1]:3210），剥掉方括号之后才能交给 net.ParseIP；
// 不带端口的 RemoteAddr（测试里常见）SplitHostPort 会失败，此时整个值
// 就是 host。
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(strings.TrimSpace(remoteAddr), "[]")
}

// tokenBuckets 是单个档位的令牌桶集合：一个 key 一个桶，懒清理。
type tokenBuckets struct {
	mu sync.Mutex

	// rate 是令牌恢复速率（个/秒），burst 是桶容量。
	rate  float64
	burst float64

	buckets map[string]*tokenBucket
	now     func() time.Time

	// 清理参数做成字段（而不是直接用常量）是为了让测试能把 TTL/间隔/
	// 阈值调小，不必为了"扫一次"而构造几千个桶或真的等十分钟。
	lastSweep      time.Time
	ttl            time.Duration
	sweepInterval  time.Duration
	sweepThreshold int
}

type tokenBucket struct {
	tokens   float64
	last     time.Time // 上次补充令牌的时刻
	lastSeen time.Time // 上次访问时刻（清理判据）
}

// newTokenBuckets 构造单档位限流器；perMinute == 0 表示该档关闭，返回 nil。
func newTokenBuckets(name string, perMinute, burst int, now func() time.Time) (*tokenBuckets, error) {
	if perMinute < 0 {
		return nil, fmt.Errorf("%s rate limit per minute must not be negative, got %d", name, perMinute)
	}
	if burst < 0 {
		return nil, fmt.Errorf("%s rate limit burst must not be negative, got %d", name, burst)
	}
	if perMinute == 0 {
		return nil, nil
	}
	if burst < 1 {
		// 速率为正、容量为 0 的桶永远拿不到令牌：配置错误但只要不检查就会
		// 静默变成"这个端点的请求全被拒"。启动时报错比上线后排查便宜得多。
		return nil, fmt.Errorf("%s rate limit burst must be at least 1 when the rate is enabled, got %d", name, burst)
	}
	return &tokenBuckets{
		rate:           float64(perMinute) / 60,
		burst:          float64(burst),
		buckets:        make(map[string]*tokenBucket),
		now:            now,
		lastSweep:      now(),
		ttl:            bucketTTL,
		sweepInterval:  bucketSweepInterval,
		sweepThreshold: bucketSweepThreshold,
	}, nil
}

// allow 尝试从 key 的桶里取一个令牌。
//
// 返回 (是否放行, 建议等待时长)。被拒绝时不扣令牌——拒绝本身不是一次
// 额度消耗；等待时长按"还差多少令牌 / 每秒恢复几个"算出来。
func (t *tokenBuckets) allow(key string) (bool, time.Duration) {
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.sweepLocked(now)

	b, ok := t.buckets[key]
	if !ok {
		// 新来源从满桶开始：burst 就是"还没被限过时能连着打几下"。
		b = &tokenBucket{tokens: t.burst, last: now}
		t.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		// 按流逝时间补充，但不超过桶容量（否则空闲很久的桶会攒出
		// 远超 burst 的令牌，一次突发就绕过设计）。
		b.tokens = math.Min(t.burst, b.tokens+elapsed*t.rate)
		b.last = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	deficit := 1 - b.tokens
	wait := time.Duration(math.Ceil(deficit / t.rate * float64(time.Second)))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return false, wait
}

// sweepLocked 按间隔或阈值清理空闲桶；调用方必须已持锁。
//
// 两个触发条件缺一不可地覆盖两种形状的滥用：来源数量缓慢增长（间隔触发，
// 保持内存平稳）与瞬间暴涨（阈值触发，立刻回收旧桶）。
func (t *tokenBuckets) sweepLocked(now time.Time) {
	if len(t.buckets) < t.sweepThreshold && now.Sub(t.lastSweep) < t.sweepInterval {
		return
	}
	for key, b := range t.buckets {
		if now.Sub(b.lastSeen) >= t.ttl {
			delete(t.buckets, key)
		}
	}
	t.lastSweep = now
}

// bucketCount 是测试用的小读取口：清理逻辑是"副作用"，没有它就只能
// 通过反射或重复构造来断言内存有没有被回收。
func (t *tokenBuckets) bucketCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buckets)
}
