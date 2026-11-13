package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是进程的启动配置，全部来自环境变量。
//
// 本地单机应用，配 Docker Compose 的 environment / .env 就够，
// 不引入配置文件的解析库。
type Config struct {
	// DatabaseURL 形如 postgres://user:pass@host:port/db?sslmode=disable
	DatabaseURL string

	// ListenAddr 是 api 的监听地址。
	//
	// 【默认只绑回环】原因见下面 defaultListenAddr 的注释。
	//
	// 【还没有任何 compose 覆盖它】现在的 deployments/docker/docker-compose.yml
	// 里只有 postgres 一个服务，api 跑在宿主机上，回环默认值正好。
	// 将来写交付期 compose（api 进容器）时必须显式设
	// CONGORAG_LISTEN_ADDR=0.0.0.0:3210 —— 容器内绑回环的话，
	// 宿主机的端口映射转发不进来，表现是连接被拒绝。
	//
	// 【为什么没有 CONGORAG_PORT 字段】那个变量属于**交付期的 compose**
	// （deployments/startup，决定宿主机映射到容器的哪个端口），不归这个
	// 进程读。这里曾经也读一次存进 Config.Port，但那个字段全仓没有任何
	// 读取点——设了它不会有任何变化，也不会收到任何提示，是个"看起来
	// 生效但不生效"的旋钮（issue #125）。改端口就改这个字段。
	ListenAddr string

	// DocumentsDir 是原始文件的存储目录（./data/documents）。
	//
	// 【两个装配根都在读它】api 与 worker 各调一次
	// knowledge.NewLocalFileStore(cfg.DocumentsDir)（apps/api/internal/app 与
	// apps/worker/internal/app）。上传时由 api 进程把原始文件写进这个根目录，
	// 处理流水线在 worker 进程里再从同一个根目录读回来——两边必须指向同一份
	// 目录，否则表现是"上传成功、处理时找不到文件"（LocalFileStore 只拿到
	// 根目录这个字符串，不会回头再读 Config，所以换目录只需改这里）。
	DocumentsDir string

	// PDFServiceURL 是科研助手 PDF 解析 sidecar 的地址（如 http://127.0.0.1:8000）。
	//
	// 【空字符串 = 关闭 PDF 通道】不设时 .pdf 上传会在 worker 处理阶段
	// 落 failed 终态（明确报"没有配置解析器"），md/txt 完全不受影响。
	// 这样默认启动行为与之前逐字节一致，不会因为部署环境没起 sidecar
	// 就让整个服务起不来。
	PDFServiceURL string

	// RetrievalDedupe 控制混合检索结果里的"近重复抑制"（同一论文多版本
	// 只保留排名最高的一条）。默认 true；CONGORAG_RETRIEVAL_DEDUP=off 时
	// 关闭——只用于离线评测做 A/B 对比。
	RetrievalDedupe bool

	// PDFTimeout 是单次 PDF 解析请求的上限。
	//
	// 【为什么必须是分钟级】541 页的《Understanding Deep Learning》官方用例
	// 全量解析实测约 2 分钟（sidecar 直跑），经过 HTTP、和 embedding 抢占
	// CPU 之后可能更久。3 分钟这种默认值会让大文档"解析超时 → River 重试 →
	// 永远卡在 processing"，是真实的坑（本机实测触发过一次）。
	PDFTimeout time.Duration

	// LogLevel 是 debug | info | warn | error
	LogLevel string

	// MasterKey 是主密钥的十六进制字符串。空字符串表示"去 MasterKeyPath 文件找"。
	//
	// 三级来源（NewSecretBox 按顺序解析）：
	//   ① 这个字段非空 → 直接用
	//   ② 为空 → 去 MasterKeyPath 读文件
	//   ③ 文件也不存在 → 随机生成一个，写回那个路径（0600），首次启动后固定下来
	MasterKey string

	// MasterKeyPath 是主密钥文件的路径，三级来源里的第②③级用它。
	//
	// 默认放在 DocumentsDir 的同级目录（./data/），理由和 DocumentsDir 一样：
	// 交付期这整个 ./data/ 目录都在 Compose 卷里，密钥文件跟着卷走，
	// 不会因为容器重建就消失、逼着所有密文一起报废。
	MasterKeyPath string

	// MaxUploadBytes 是单次上传允许的最大请求体字节数（含 multipart 的
	// 边界与各个 part 的头部，比文件本身略小一点）。
	//
	// 【为什么必须有这个上限】net/http 的 FormFile 内部走
	// ParseMultipartForm(32<<20)：请求体超过 32MB 的部分会被完整读进来、
	// 溢写到 os.TempDir，然后才轮得到业务代码去拒绝它；worker 侧还会把
	// 落盘后的整个文件 io.ReadAll 进内存。没有上限时，一个几 GB 的请求体
	// 在任何拒绝点存在之前就已经被吃完了。
	MaxUploadBytes int64

	// TiktokenCacheDir 是 tiktoken BPE 词表的本地缓存目录。
	//
	// weaviate/tiktoken-go 首次加载某个 encoding 时要从
	// openaipublic.blob.core.windows.net 下载几百 KB 的词表文件，
	// 之后走本地缓存（读 TIKTOKEN_CACHE_DIR 环境变量，见 tokenizer.go）。
	// 放进 ./data/ 而不是系统临时目录，理由和 MasterKeyPath 一样：
	// 跟着 Compose 卷走，不会因为容器重建就重新触发一次下载。
	TiktokenCacheDir string

	// RateLimitPerMinute / RateLimitBurst 是 HTTP 限流的 general 档：
	// 所有 /api/ 请求共用，默认每分钟 240 个令牌、桶容量 60。0 表示关闭
	// 这一档（语义见 RateLimitConfig 的注释）。
	//
	// 【为什么默认开着】限流是这一轮的交付物之一；默认关掉的防御只有
	// "文档里写了可以开"，评审看到的就是一个不生效的旋钮。默认值按
	// "一个正常用户不会碰到"来定，遇到再调。
	RateLimitPerMinute int
	RateLimitBurst     int

	// RateLimitLLMPerMinute / RateLimitLLMBurst 是 expensive 档：会调
	// 模型/embedding 或触发大规模重索引的端点走这一档，默认 30/min、
	// burst 8——比 general 严格一个数量级，因为一次调用的真实成本高。
	RateLimitLLMPerMinute int
	RateLimitLLMBurst     int
}

// 默认值。每一项都能用对应的 CONGORAG_* 环境变量覆盖。
const (
	// 【默认绑回环，不绑 0.0.0.0】
	// 方案 §1 的部署边界是本地单机应用，OriginCheck 中间件也只放行回环的 Host。
	// 绑 0.0.0.0 的话，端口对整个局域网都是打开的——虽然请求会被 OriginCheck 挡成
	// 403，但"端口开着"和"端口不存在"是两回事：前者仍然暴露了服务指纹，
	// 也把安全性全压在一个中间件上。
	//
	// 代价记在这里：这个默认值把"api 进容器"这件事变成了必须显式配置的操作。
	// 写交付期 compose 时漏了 CONGORAG_LISTEN_ADDR=0.0.0.0:3210，
	// 容器会正常启动、日志会打印 listening，但外面连不进来。
	defaultListenAddr       = "127.0.0.1:3210"
	defaultDocumentsDir     = "./data/documents"
	defaultLogLevel         = "info"
	defaultMasterKeyPath    = "./data/master.key"
	defaultTiktokenCacheDir = "./data/tiktoken-cache"

	// 32 MiB。够放下本项目面向的 md/txt 文档（一份 3MB 的中文文本已经能切
	// 出几千个分块），又小到不会把 worker 的内存和用户的 embedding 额度吃掉。
	defaultMaxUploadBytes = 32 << 20

	// 10 分钟。比 541 页实测的 2 分钟留 5 倍余量；真正更慢的解析应该
	// 走后台任务而不是拉长 HTTP 超时。
	defaultPDFTimeout = 10 * time.Minute

	// 限流的默认额度（单位：令牌/分钟；burst 是桶容量）。
	//
	// general 240/min（4/s）对"一个用户点界面"足够宽松：页面静态资源
	// 不受限（见 RateLimit 的注释），/api/ 请求不会连续打到每秒 4 个以上；
	// burst 60 允许一次刷新后的并发请求全部通过。
	//
	// expensive 30/min（0.5/s）、burst 8：一次对话/写作/对比就是一次模型
	// 调用，30/min 足够一个人正常使用；burst 8 允许"点一下写作、马上切
	// 对比"这种短时连点。
	defaultRateLimitPerMinute    = 240
	defaultRateLimitBurst        = 60
	defaultRateLimitLLMPerMinute = 30
	defaultRateLimitLLMBurst     = 8
)

// LoadConfig 从环境变量读配置，缺失的用默认值补。
//
// 只有 DatabaseURL 是必填的——没有它服务起不来，而且猜不出正确值。
func LoadConfig() (*Config, error) {
	// 【限流参数为什么特殊对待】别的数值配置（超时、上传上限）非法时静默
	// 退回默认值，因为"有一个合理的默认行为"比"起不来"重要；限流相反：
	// 一个手滑的负数或拼错的变量名会让防线无声消失，而那正是这一轮的
	// 交付物。所以这 4 个变量解析失败/为负一律 return error，让进程在
	// 启动时就地失败，而不是带着"看起来配了"的配置跑起来。
	generalPerMinute, err := envNonNegativeInt("CONGORAG_RATE_LIMIT_PER_MINUTE", defaultRateLimitPerMinute)
	if err != nil {
		return nil, err
	}
	generalBurst, err := envNonNegativeInt("CONGORAG_RATE_LIMIT_BURST", defaultRateLimitBurst)
	if err != nil {
		return nil, err
	}
	llmPerMinute, err := envNonNegativeInt("CONGORAG_RATE_LIMIT_LLM_PER_MINUTE", defaultRateLimitLLMPerMinute)
	if err != nil {
		return nil, err
	}
	llmBurst, err := envNonNegativeInt("CONGORAG_RATE_LIMIT_LLM_BURST", defaultRateLimitLLMBurst)
	if err != nil {
		return nil, err
	}

	// 0 是"关闭这一档"；但"开着却 burst=0"是自相矛盾的配置——桶永远
	// 拿不到令牌，表现成该端点全被拒。启动时报错，别等上线后排查。
	if generalPerMinute > 0 && generalBurst < 1 {
		return nil, fmt.Errorf("CONGORAG_RATE_LIMIT_BURST must be at least 1 when CONGORAG_RATE_LIMIT_PER_MINUTE is enabled, got %d", generalBurst)
	}
	if llmPerMinute > 0 && llmBurst < 1 {
		return nil, fmt.Errorf("CONGORAG_RATE_LIMIT_LLM_BURST must be at least 1 when CONGORAG_RATE_LIMIT_LLM_PER_MINUTE is enabled, got %d", llmBurst)
	}

	cfg := &Config{
		DatabaseURL:      os.Getenv("CONGORAG_DB_URL"),
		ListenAddr:       envOr("CONGORAG_LISTEN_ADDR", defaultListenAddr),
		PDFServiceURL:    os.Getenv("CONGORAG_PDF_SERVICE_URL"),
		RetrievalDedupe:  !strings.EqualFold(strings.TrimSpace(os.Getenv("CONGORAG_RETRIEVAL_DEDUP")), "off"),
		PDFTimeout:       envDuration("CONGORAG_PDF_TIMEOUT", defaultPDFTimeout),
		DocumentsDir:     envOr("CONGORAG_DOCUMENTS_DIR", defaultDocumentsDir),
		LogLevel:         envOr("CONGORAG_LOG_LEVEL", defaultLogLevel),
		MasterKey:        os.Getenv("CONGORAG_MASTER_KEY"),
		MasterKeyPath:    envOr("CONGORAG_MASTER_KEY_PATH", defaultMasterKeyPath),
		TiktokenCacheDir: envOr("CONGORAG_TIKTOKEN_CACHE_DIR", defaultTiktokenCacheDir),
		MaxUploadBytes:   envBytes("CONGORAG_MAX_UPLOAD_BYTES", defaultMaxUploadBytes),

		RateLimitPerMinute:    generalPerMinute,
		RateLimitBurst:        generalBurst,
		RateLimitLLMPerMinute: llmPerMinute,
		RateLimitLLMBurst:     llmBurst,
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("CONGORAG_DB_URL is required")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envBytes 读一个正整数（字节数），空值、解析失败和非正数都退回默认值。
//
// 【为什么不用 envOr + 就地 ParseInt】配错一个环境变量不该让服务起不来，
// 但也不能让它静默地把上限关掉：0 在调用侧的约定是"不限"（见
// api.Deps.MaxUploadBytes），一个手滑打进去的 0 会让防线无声消失——而那正是
// 这个配置项存在的意义。所以空值之外的非法值一律退回默认值，宁可保住限。
func envBytes(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// envDuration 读一个 time.ParseDuration 格式的环境变量；缺省或非法时用默认值。
//
// 【非法值为什么静默回退】配置项错一个字符就让整个服务起不来，对单机
// 应用代价太大；而 PDF 超时是一个"有合理默认值的性能参数"，不是连接串
// 那种"错了必然不可用"的必填项。非法值会在下次调用时表现得和没配一样，
// 不会造成数据错误。
func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// envNonNegativeInt 读一个"允许 0、不允许负数/非数字"的整数环境变量。
//
// 【为什么和 envBytes 的做法不同】envBytes 对非法值退回默认值，判据是
// "这个配置有合理的默认行为"（上传上限）。限流不满足这个判据：把用户
// 写错的负数悄悄换成默认值，等于告诉他"你配的额度生效了"，而实际生效的
// 是另一个数。所以这里的选择是：没设置 → 默认值；设置了但解析不出非负
// 整数 → 启动失败并指名变量。
func envNonNegativeInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", key, n)
	}
	return n, nil
}
