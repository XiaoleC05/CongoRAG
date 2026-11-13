// Package api 是 HTTP 边界。
//
// 这是唯一 import gin 的业务侧包；internal/ 下不写 handler、不用 net/http 的
// 状态码表达 API 语义。唯一的结构例外是 platform 的中间件会用 gin（它就在
// HTTP 边界上），例外清单与守卫见 internal/arch_test.go。
//
// 路由不用手写：generated.go 里的 RegisterHandlers 按 contracts/openapi.yaml
// 把每个 URL 挂到 ServerInterface 对应的方法上。
//
// 【本文件只放"外壳"】依赖注入（Deps / Server / NewServer）、健康检查、
// 以及所有 handler 共用的请求工具（分页参数、JSON 绑定、响应体上限）。
// 具体端点按资源域放在同包的 handler_*.go 里，纯 DTO 转换在 convert.go。
// 这样拆的理由：server.go 曾经是 2000+ 行的单文件，六个资源域的 handler、
// 转换函数、prompt 拼装全挤在一起，改任何一处都要在同一个文件里翻几百行。
// 按"一个资源域一个文件"切开之后，改知识库只会碰 handler_knowledge.go，
// review 的 diff 也只落在那一个文件上。
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/agent"
	"github.com/XiaoleC05/CongoRAG/internal/arxiv"
	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/recommend"
	"github.com/XiaoleC05/CongoRAG/internal/retrieval"
)

// Deps 是 Server 需要的全部依赖，由装配根填入。
// 后续里程碑每加一个模块就往这里加一个字段。
type Deps struct {
	Logger       *slog.Logger
	Knowledge    *knowledge.Usecase
	LLM          *llm.Usecase
	Conversation *conversation.Usecase
	Agent        *agent.Usecase

	// Files 是原始文件存储，只有下载端点（GET /documents/{id}/file）用。
	// 类型是接口而不是 *knowledge.LocalFileStore：HTTP 边界不该知道文件是
	// 落在本地磁盘还是对象存储（将来换 S3 只换装配根）。
	Files knowledge.FileStore

	// Retrieval 是检索调试视图（POST /knowledge-bases/{id}/search）用的。
	// 它和 knowledge 是不同的关注点：knowledge 管知识库与文档的 CRUD，
	// 检索是它下面那一层（retrieval 才是向量的读写方）。
	Retrieval *retrieval.Usecase

	// Arxiv / ArxivVotes / Querier 是 M4 的 arXiv 推荐、反馈与下钻入库用的。
	// Arxiv 由装配根构造（可注入假实现做测试），Votes 只需要一个 pg 查询器。
	Arxiv      *arxiv.Client
	ArxivVotes *recommend.Store
	Querier    platform.Querier

	// MaxUploadBytes 是上传接口允许的最大请求体字节数（platform.Config
	// 的对应项）。只有 UploadDocument 用它，<=0 表示不限——生产装配里
	// 永远是一个正数。
	MaxUploadBytes int64

	// DB 是就绪探针要探测的那个依赖，只有 Readyz 用它。
	//
	// 类型是 platform.Pinger 而不是 *pgxpool.Pool：这个包至今没有 import
	// 过任何数据库驱动，Deps 是它唯一能拿到池的口子，这里一写具体类型，
	// 驱动就顺着 Deps 进了 HTTP 边界。
	DB platform.Pinger
}

// 编译期断言：契约里加了端点而这里没实现，编译失败。
var _ ServerInterface = (*Server)(nil)

type Server struct {
	deps Deps
}

func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// Healthz 只报告进程存活，不检查数据库。
//
// 数据库连不上时服务本身是活的；是否该给它发流量由 readiness 探针判断。
// 两者混在一起会导致数据库抖动时实例被反复重启。
func (s *Server) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readinessProbeTimeout 是 /readyz 探测数据库的上限。
//
// 【必须有这个超时】pgxpool.Ping 内部是 Acquire（可能要新建连接）+ 一次
// 往返，两者都只认 ctx；连接串里没写 connect_timeout 时 pgx 不设
// ConnectTimeout，一个被防火墙丢包的数据库会让 Ping 一直挂着。探针要回答
// 的是"现在能不能发流量"，不是"等到有答案"。
const readinessProbeTimeout = 2 * time.Second

// parseListParams 把三个分页端点共用的 limit / cursor 解出来（issue #45）。
//
// 【为什么在 handler 里校验】"参数越界返回 400"是 HTTP 边界的语义。
// usecase 里也会再校验一遍（它可能被别的调用方直接调），那是纵深不是重复。
//
// 返回的 bool 为 false 表示已经写过响应了，调用方直接 return。
func (s *Server) parseListParams(c *gin.Context, limit *Limit, cursor *Cursor) (int, string, bool) {
	n := 0
	if limit != nil {
		n = int(*limit)
	}
	n, err := platform.ClampListLimit(n)
	if err != nil {
		s.fail(c, err)
		return 0, "", false
	}

	cur := ""
	if cursor != nil {
		cur = strings.TrimSpace(string(*cursor))
	}
	return n, cur, true
}

// nullableCursor 把内部用的空串转成契约里的 null / 缺省。
//
// 【空串在内部表示"没有更多"】契约里这个字段是 nullable 的字符串，前端拿
// 到 null 就该把「加载更多」收起来。用空串表达的话前端要多判一种情况，
// 而且空串是一个合法的字符串值，语义上区分不干净。
func nullableCursor(next string) *string {
	if next == "" {
		return nil
	}
	return &next
}

// maxJSONBodyBytes 是 JSON 端点允许的最大请求体字节数。
//
// 【为什么需要它】ShouldBindJSON 内部是 json.Decoder，读到 EOF 才停。没有
// 上限时一个几百 MB 的 body（本机脚本跑飞、前端 bug）会被整个解进内存。
// 上传那条路早就有上限了（Deps.MaxUploadBytes，见 UploadDocument 的注释），
// 其余端点一直没有——config.go 里"没有上限时几 GB 的请求体在任何拒绝点存在
// 之前就已经被吃完"那条论证对它们同样成立。
//
// 【为什么是常量，不是 platform.Config 的一项】上传上限来自部署配置，因为它
// 决定"用户能把多大的文件交进来"，是对外承诺；这个值只是"一条 HTTP 请求该
// 有多小"的防御线。1 MiB 对任何 JSON 端点都远远够用（最长的字段是聊天正文，
// 前端按字符数计），没有按部署调整的理由——写成常量也就只有一处可看。
//
// 【SSE 的 POST 端点也走这条路】SendMessage / StartAgentRun 的响应是流，
// 请求体却是普通 JSON：限的是**读**，不影响写，1 MiB 也不是流式响应的限制。
//
// 【为什么 1 MiB 而不是更小】不能小到把合法请求挡在门外。这个值是"整条 body
// 的上限"而不是"某个字段的上限"，所以判断依据是最宽的那个端点（agents 的
// instruction）——1 MiB 大约是 30 万汉字，任何手写配置都到不了。
const maxJSONBodyBytes int64 = 1 << 20

// bindJSON 把请求体绑进 dst；失败时写好 400 响应并返回 false。
//
// 【为什么收敛成一个方法】这段样板原来散在十个 handler 里，承载的是一条策略：
// "绑定失败 → 400 + 固定文案 +（对 SSE 端点）必须在 newSSESink 之前"。
// 散在十处时，改文案或加请求体上限漏掉一处不会有任何编译错误或测试失败。
// 现在两点都只加在这一处，十个调用点自动一致。
//
// 【返回值的约定】false 表示响应已经写过，调用方直接 return——和
// parseListParams 同一个形状。SendMessage / StartAgentRun 要特别注意：
// 这次调用必须在 newSSESink 之前，sink 一构造出来响应就切成 200 + SSE 了，
// 那时再想回一个 400 Problem 已经来不及。
func (s *Server) bindJSON(c *gin.Context, dst any) bool {
	// 【MaxBytesReader 必须在绑定之前包上】ShouldBindJSON 会一路读到 EOF；
	// 等它返回再判长度，超限的 body 已经整个进过内存了——这正是
	// UploadDocument 里"上限必须在 FormFile 之前设"的同一件事。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes)

	if err := c.ShouldBindJSON(dst); err != nil {
		// 【超限和"不是合法 JSON"要分开报】MaxBytesReader 超限返回的是
		// *http.MaxBytesError，文案（"http: request body too large"）是标准库
		// 内部的说法，对客户端没有意义——换成一句带上限值的，和上传那条路
		// 一致。gin 的 json 绑定把 Decoder 的错误原样返回、不包一层，
		// 所以 errors.As 能直接拿到它。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(c, fmt.Errorf("request body exceeds the %d byte limit: %w",
				tooLarge.Limit, platform.ErrInvalid))
			return false
		}
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return false
	}
	return true
}

// Readyz 是就绪探针：进程活着之外，还要求数据库能用。
//
// 【探测什么、不探测什么】只探这个进程服务每一个请求都离不开的东西，
// 也就是连接池。不探：
//   - schema_migrations 的版本：它不在请求路径上（运行时没有代码读它），
//     一次没跑完或 dirty 的迁移是运维要处理的事，不是"这个实例不能干活"。
//     接进来等于把一个部署期状态变成永远好不了的 503。
//   - River 的队列表：api 侧只是 insert-only，缺表只影响上传这一条路径，
//     而 /readyz 是一刀切的门禁——判它不 ready 会把浏览、聊天一起下线。
//   - 任何出网的调用（模型/embedding 服务）：它们是每请求的，而且已经以
//     upstream_llm_error(502) 的形式失败；用探针每几秒去敲一次第三方，
//     等于把对方的抖动变成"你本地应用挂了"。
//
// 【失败为什么不走 s.fail】fail 的 5xx 分支会把 detail 换成"服务内部错误"
// ——那恰恰是就绪探针最需要说清楚的信息（是数据库，不是别的），换掉之后
// 运维只能去翻日志；而且它按 Error 级别记日志，一个几秒一次的探针会把日志
// 淹掉。这里直接 writeProblem，和 spa.go 的 NoRoute 404 是同一类
// "不是请求失败，而是状态报告"。
func (s *Server) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessProbeTimeout)
	defer cancel()

	if err := s.deps.DB.Ping(ctx); err != nil {
		s.deps.Logger.Warn("readiness probe failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"error", err,
		)
		typ, title, detail := "unavailable", "服务暂不可用", "数据库连接不可用"
		writeProblem(c, http.StatusServiceUnavailable, Problem{
			Type: typ, Status: http.StatusServiceUnavailable, Title: &title, Detail: &detail,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
