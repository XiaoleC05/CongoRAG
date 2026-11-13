package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 限流是"放宽了不会报错、只会静默失效"的那类防御：桶大小错了、两档串了、
// 429 形状不对，测试不盯就全靠线上撞。下面按行为分组钉死。

// fakeClock 是可控时钟：令牌桶的"等待后恢复"与空闲清理都不该真的等。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// okHandler 返回 200；同时记录它被调用了几次（被限流的请求不该进 handler）。
func okHandler(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

func newRateLimitRouter(limiter *RateLimiter, register func(*gin.Engine)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(limiter))
	register(r)
	return r
}

func doRateReq(r *gin.Engine, method, path, remoteAddr, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ────────────────────────────────────────────────────────────────
// 令牌桶本身
// ────────────────────────────────────────────────────────────────

func TestTokenBuckets_AllowBlockRecover(t *testing.T) {
	clock := newFakeClock()
	// 60/min = 每秒 1 个令牌，桶容量 2：连打两下允许，第三下拒绝，等 1 秒恢复。
	store, err := newTokenBuckets("test", 60, 2, clock.now)
	require.NoError(t, err)

	assert.True(t, mustAllow(t, store, "k"), "第 1 个令牌应放行")
	assert.True(t, mustAllow(t, store, "k"), "第 2 个令牌应放行")

	allowed, wait := store.allow("k")
	assert.False(t, allowed, "桶空后应拒绝")
	assert.InDelta(t, 1.0, wait.Seconds(), 0.05, "等待时长应约等于 1 个令牌的补充时间")

	// 拒绝不扣令牌：再拒绝一次，等待时长不应变长。
	_, waitAgain := store.allow("k")
	assert.InDelta(t, wait.Seconds(), waitAgain.Seconds(), 0.05, "被拒的请求不应消耗额度")

	clock.advance(500 * time.Millisecond)
	allowed, wait = store.allow("k")
	assert.False(t, allowed, "半秒只补半个令牌，仍应拒绝")
	assert.InDelta(t, 0.5, wait.Seconds(), 0.05)

	clock.advance(500 * time.Millisecond)
	assert.True(t, mustAllow(t, store, "k"), "补满 1 个令牌后应恢复")
}

func TestTokenBuckets_CapsAtBurst(t *testing.T) {
	clock := newFakeClock()
	// 600/min = 每秒 10 个令牌，容量 3：空闲再久也只能攒 3 个。
	store, err := newTokenBuckets("test", 600, 3, clock.now)
	require.NoError(t, err)
	require.True(t, mustAllow(t, store, "k"))

	clock.advance(10 * time.Second)
	// 容量 3：空闲再久也只能攒 3 个令牌，因此接下来恰好 3 次放行、第 4 次拒绝。
	assert.True(t, mustAllow(t, store, "k"))
	assert.True(t, mustAllow(t, store, "k"))
	assert.True(t, mustAllow(t, store, "k"))
	assert.False(t, mustAllow(t, store, "k"), "空闲再久也不能攒出超过 burst 的令牌")
}

func mustAllow(t *testing.T, store *tokenBuckets, key string) bool {
	t.Helper()
	allowed, _ := store.allow(key)
	return allowed
}

func TestTokenBuckets_CleanupByInterval(t *testing.T) {
	clock := newFakeClock()
	store, err := newTokenBuckets("test", 60, 1, clock.now)
	require.NoError(t, err)
	// 把清理参数调小，避免测试为了"扫一次"等十分钟。
	store.ttl = time.Minute
	store.sweepInterval = 10 * time.Second

	for _, key := range []string{"a", "b", "c"} {
		require.True(t, mustAllow(t, store, key))
	}
	require.Equal(t, 3, store.bucketCount())

	// 空闲超过 TTL 后，下一个请求触发按间隔的扫描：旧桶全回收。
	clock.advance(2 * time.Minute)
	require.True(t, mustAllow(t, store, "d"))
	assert.Equal(t, 1, store.bucketCount(), "空闲超过 TTL 的桶必须被回收，否则内存随来源数无界增长")
}

func TestTokenBuckets_CleanupByThreshold(t *testing.T) {
	clock := newFakeClock()
	store, err := newTokenBuckets("test", 60, 1, clock.now)
	require.NoError(t, err)
	// 间隔设得极长（不会触发），只靠"桶数达到阈值"触发回收。
	store.ttl = time.Minute
	store.sweepInterval = time.Hour
	store.sweepThreshold = 2

	for _, key := range []string{"a", "b", "c"} {
		require.True(t, mustAllow(t, store, key))
	}
	clock.advance(2 * time.Minute)

	require.True(t, mustAllow(t, store, "d"))
	assert.Equal(t, 1, store.bucketCount(), "桶数达到阈值时必须立即扫描，不能等间隔到期")
}

// ────────────────────────────────────────────────────────────────
// 配置校验
// ────────────────────────────────────────────────────────────────

func TestNewRateLimiter_ValidatesConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cfg     RateLimitConfig
		wantErr string
	}{
		{"负数速率", RateLimitConfig{GeneralPerMinute: -1, GeneralBurst: 1}, "must not be negative"},
		{"负数容量", RateLimitConfig{GeneralPerMinute: 1, GeneralBurst: -1}, "must not be negative"},
		{"开着但容量为 0", RateLimitConfig{GeneralPerMinute: 1, GeneralBurst: 0}, "at least 1"},
		{"expensive 开着但容量为 0", RateLimitConfig{GeneralPerMinute: 0, LLMPerMinute: 1, LLMBurst: 0}, "at least 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limiter, err := NewRateLimiter(tt.cfg)
			require.Error(t, err)
			assert.Nil(t, limiter)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	// 0 是合法值（关闭这一档）；两档都 0 也合法，只是不限流。
	limiter, err := NewRateLimiter(RateLimitConfig{})
	require.NoError(t, err)
	require.NotNil(t, limiter)
	assert.Nil(t, limiter.general)
	assert.Nil(t, limiter.expensive)
}

// ────────────────────────────────────────────────────────────────
// expensive 路由表
// ────────────────────────────────────────────────────────────────

func TestIsExpensiveRequest(t *testing.T) {
	// 触发模型/embedding/大规模重索引的 POST 端点必须都在表里。
	expensive := []string{
		"/api/v1/conversations/:id/messages",
		"/api/v1/agents/:id/runs",
		"/api/v1/runs/:runId/resume",
		"/api/v1/translate",
		"/api/v1/visualize",
		"/api/v1/knowledge-bases/:id/writing",
		"/api/v1/knowledge-bases/:id/compare",
		"/api/v1/knowledge-bases/:id/citation-survey",
		"/api/v1/knowledge-bases/:id/arxiv-ingest",
		"/api/v1/knowledge-bases/:id/search",
		"/api/v1/documents/:id/version-diff",
		"/api/v1/documents/:id/reindex",
		"/api/v1/knowledge-bases/:id/reindex",
		"/api/v1/providers",
	}
	for _, path := range expensive {
		assert.True(t, isExpensiveRequest(http.MethodPost, path), "%s 应走 expensive 档", path)
	}

	// 反例：列表/普通 CRUD 不该被 expensive 档限制——把日常操作也压到
	// 30/min 会让界面不可用，而且"昂贵"这个词就失去意义了。
	notExpensive := [][2]string{
		{http.MethodPost, "/api/v1/knowledge-bases"},                    // 建库是本地 INSERT
		{http.MethodPost, "/api/v1/conversations"},                      // 建会话是本地 INSERT
		{http.MethodPost, "/api/v1/documents/:id/duplicate-resolution"}, // 只改库
		{http.MethodGet, "/api/v1/knowledge-bases"},                     // 列表永远是 GET
		{http.MethodGet, "/api/v1/translate"},                           // 错方法不是昂贵端点
		{http.MethodGet, "/api/v1/providers"},                           // 列 provider 不花模型
		{http.MethodPost, "/api/v1/not-exist"},                          // 404 走 general
	}
	for _, tc := range notExpensive {
		assert.False(t, isExpensiveRequest(tc[0], tc[1]), "%s %s 不该走 expensive 档", tc[0], tc[1])
	}
}

// ────────────────────────────────────────────────────────────────
// 429 响应形状
// ────────────────────────────────────────────────────────────────

func TestRateLimit_GeneralTierReturns429Problem(t *testing.T) {
	clock := newFakeClock()
	// general 60/min burst 1：同一个来源第二个请求就超限。
	limiter, err := newRateLimiter(RateLimitConfig{GeneralPerMinute: 60, GeneralBurst: 1}, clock.now)
	require.NoError(t, err)

	var calls int32
	r := newRateLimitRouter(limiter, func(r *gin.Engine) {
		r.GET("/api/v1/knowledge-bases", func(c *gin.Context) {
			atomic.AddInt32(&calls, 1)
			okHandler(c)
		})
	})

	first := doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.1.2.3:5555", "")
	require.Equal(t, http.StatusOK, first.Code)

	second := doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.1.2.3:5555", "")
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "被限流的请求不能再进 handler")

	// 契约/前端共同依赖的形状：problem+json + Problem{type,status,title,detail}。
	assert.Equal(t, ProblemContentType, second.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", second.Header().Get("Cache-Control"), "错误响应不能被缓存")
	assert.Equal(t, "1", second.Header().Get("Retry-After"), "Retry-After 必须是整秒")

	var p Problem
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &p))
	assert.Equal(t, problemTypeRateLimited, p.Type)
	assert.Equal(t, http.StatusTooManyRequests, p.Status)
	assert.Equal(t, rateLimitedTitle, p.Title)
	assert.Contains(t, p.Detail, "秒后重试", "detail 要带可操作的重试秒数")
	assert.Contains(t, p.Detail, second.Header().Get("Retry-After"),
		"正文里的秒数和 Retry-After 必须一致，否则按 header 退避的人等不到恢复")
}

func TestRateLimit_RealClockRecoversAfterWaiting(t *testing.T) {
	// 一条不看假时钟的端到端恢复：60/min = 1 个令牌/秒，burst 1。
	// 选 1s 的恢复间隔而不是毫秒级，是为了在 CI 负载下也不误报——
	// 毫秒级间隔里"第二个请求到达时可能已经补满"，测试会间歇性变绿。
	limiter, err := NewRateLimiter(RateLimitConfig{GeneralPerMinute: 60, GeneralBurst: 1})
	require.NoError(t, err)
	r := newRateLimitRouter(limiter, func(r *gin.Engine) {
		r.GET("/api/v1/healthz", okHandler)
	})

	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/healthz", "10.9.9.9:1", "").Code)
	require.Equal(t, http.StatusTooManyRequests, doRateReq(r, http.MethodGet, "/api/v1/healthz", "10.9.9.9:1", "").Code)

	time.Sleep(1100 * time.Millisecond)
	assert.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/healthz", "10.9.9.9:1", "").Code,
		"等待超过一个令牌的补充时间后必须恢复放行")
}

// ────────────────────────────────────────────────────────────────
// 两档独立 / 关闭语义
// ────────────────────────────────────────────────────────────────

func TestRateLimit_TiersUseIndependentBuckets(t *testing.T) {
	clock := newFakeClock()
	// 两档都是 burst 2，但桶互相独立：expensive 打满不影响 general。
	limiter, err := newRateLimiter(RateLimitConfig{
		GeneralPerMinute: 60, GeneralBurst: 2,
		LLMPerMinute: 60, LLMBurst: 2,
	}, clock.now)
	require.NoError(t, err)
	r := newRateLimitRouter(limiter, func(r *gin.Engine) {
		r.GET("/api/v1/knowledge-bases", okHandler)
		r.POST("/api/v1/translate", okHandler)
	})

	// expensive 连打两下用光它的桶，第三下 429。
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.0.0.1:1", "").Code)
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.0.0.1:1", "").Code)
	require.Equal(t, http.StatusTooManyRequests, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.0.0.1:1", "").Code)

	// general 的桶完全没被扣过：同一来源还能再放行两次。
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.0.0.1:1", "").Code)
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.0.0.1:1", "").Code)
	assert.Equal(t, http.StatusTooManyRequests, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.0.0.1:1", "").Code)
}

func TestRateLimit_DisabledTiers(t *testing.T) {
	clock := newFakeClock()

	t.Run("只开 general 时 expensive 端点回落到 general", func(t *testing.T) {
		limiter, err := newRateLimiter(RateLimitConfig{GeneralPerMinute: 60, GeneralBurst: 1}, clock.now)
		require.NoError(t, err)
		r := newRateLimitRouter(limiter, func(r *gin.Engine) {
			r.POST("/api/v1/translate", okHandler)
		})
		require.Equal(t, http.StatusOK, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.2.2.2:1", "").Code)
		assert.Equal(t, http.StatusTooManyRequests, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.2.2.2:1", "").Code,
			"expensive 关闭不等于昂贵端点裸奔：general 仍是它的兜底")
	})

	t.Run("两档都关完全不限流", func(t *testing.T) {
		limiter, err := newRateLimiter(RateLimitConfig{}, clock.now)
		require.NoError(t, err)
		r := newRateLimitRouter(limiter, func(r *gin.Engine) {
			r.GET("/api/v1/knowledge-bases", okHandler)
			r.POST("/api/v1/translate", okHandler)
		})
		for i := 0; i < 10; i++ {
			require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.3.3.3:1", "").Code)
			require.Equal(t, http.StatusOK, doRateReq(r, http.MethodPost, "/api/v1/translate", "10.3.3.3:1", "").Code)
		}
	})
}

// ────────────────────────────────────────────────────────────────
// 适用路径
// ────────────────────────────────────────────────────────────────

func TestRateLimit_OnlyAppliesToAPIPaths(t *testing.T) {
	clock := newFakeClock()
	limiter, err := newRateLimiter(RateLimitConfig{GeneralPerMinute: 60, GeneralBurst: 1}, clock.now)
	require.NoError(t, err)
	r := newRateLimitRouter(limiter, func(r *gin.Engine) {
		r.GET("/assets/app.js", okHandler)
		r.GET("/", okHandler)
		r.GET("/api/v1/knowledge-bases", okHandler)
	})

	// 页面一次加载会并发取几十个静态文件；把它们算进额度会让"打开页面"
	// 本身就把额度打满。
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/assets/app.js", "10.4.4.4:1", "").Code)
		require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/", "10.4.4.4:1", "").Code)
	}
	// /api/ 仍然受限。
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.4.4.4:1", "").Code)
	assert.Equal(t, http.StatusTooManyRequests, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "10.4.4.4:1", "").Code)
}

func TestRateLimit_NilLimiterIsPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(nil))
	r.GET("/api/v1/x", okHandler)
	assert.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/x", "10.5.5.5:1", "").Code)
}

// ────────────────────────────────────────────────────────────────
// clientKey：来源识别
// ────────────────────────────────────────────────────────────────

func TestClientKey(t *testing.T) {
	for _, tt := range []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{
			name:       "非回环对端不信任 XFF",
			remoteAddr: "203.0.113.9:5555",
			xff:        "198.51.100.7",
			want:       "203.0.113.9",
		},
		{
			name:       "回环对端取 XFF 第一跳（nginx 反代）",
			remoteAddr: "127.0.0.1:41234",
			xff:        "198.51.100.7, 10.0.0.1, 10.0.0.2",
			want:       "198.51.100.7",
		},
		{
			name:       "回环对端但 XFF 只有空白：退回回环地址",
			remoteAddr: "127.0.0.1:41234",
			xff:        "   ",
			want:       "127.0.0.1",
		},
		{
			name:       "回环对端但 XFF 第一跳为空：退回回环地址",
			remoteAddr: "127.0.0.1:41234",
			xff:        " , 10.0.0.1",
			want:       "127.0.0.1",
		},
		{
			name:       "IPv6 回环对端同样允许 XFF",
			remoteAddr: "[::1]:41234",
			xff:        "203.0.113.20",
			want:       "203.0.113.20",
		},
		{
			name:       "无 XFF 时用回环地址",
			remoteAddr: "127.0.0.1:41234",
			want:       "127.0.0.1",
		},
		{
			name:       "RemoteAddr 不带端口也能解析",
			remoteAddr: "203.0.113.30",
			want:       "203.0.113.30",
		},
		{
			name:       "RemoteAddr 为空给稳定占位键",
			remoteAddr: "",
			want:       "unknown",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			assert.Equal(t, tt.want, clientKey(req))
		})
	}
}

// 同一个伪造 XFF 的直连客户端不应该因为换头而被分到新桶——这是"不能
// 盲信 XFF"的核心断言，用它把上面那条单测固定成安全性质。
func TestRateLimit_ForgedXFFDoesNotResetQuota(t *testing.T) {
	clock := newFakeClock()
	limiter, err := newRateLimiter(RateLimitConfig{GeneralPerMinute: 60, GeneralBurst: 1}, clock.now)
	require.NoError(t, err)
	r := newRateLimitRouter(limiter, func(r *gin.Engine) {
		r.GET("/api/v1/knowledge-bases", okHandler)
	})

	// 直连来源不是回环：XFF 完全被忽略，前两个请求共用一个桶。
	require.Equal(t, http.StatusOK, doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "203.0.113.50:3333", "1.1.1.1").Code)
	assert.Equal(t, http.StatusTooManyRequests,
		doRateReq(r, http.MethodGet, "/api/v1/knowledge-bases", "203.0.113.50:3333", "2.2.2.2").Code,
		"换一个伪造的 XFF 不应该得到新桶")
}
