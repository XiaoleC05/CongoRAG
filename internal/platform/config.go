package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
)

// LoadConfig 从环境变量读配置，缺失的用默认值补。
//
// 只有 DatabaseURL 是必填的——没有它服务起不来，而且猜不出正确值。
func LoadConfig() (*Config, error) {
	cfg := &Config{
		DatabaseURL:      os.Getenv("CONGORAG_DB_URL"),
		ListenAddr:       envOr("CONGORAG_LISTEN_ADDR", defaultListenAddr),
		DocumentsDir:     envOr("CONGORAG_DOCUMENTS_DIR", defaultDocumentsDir),
		LogLevel:         envOr("CONGORAG_LOG_LEVEL", defaultLogLevel),
		MasterKey:        os.Getenv("CONGORAG_MASTER_KEY"),
		MasterKeyPath:    envOr("CONGORAG_MASTER_KEY_PATH", defaultMasterKeyPath),
		TiktokenCacheDir: envOr("CONGORAG_TIKTOKEN_CACHE_DIR", defaultTiktokenCacheDir),
		MaxUploadBytes:   envBytes("CONGORAG_MAX_UPLOAD_BYTES", defaultMaxUploadBytes),
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
