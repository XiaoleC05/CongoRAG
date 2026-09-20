// secretbox.go 实现 API Key 落盘前的加解密。
//
// 这一段代码在项目里的位置特殊：它的失败模式几乎全部不报错。
// nonce 复用、密钥长度不对但凑巧跑起来、文件权限开太大——这些错误
// 都会让程序正常编译、正常启动、正常加解密出看起来合理的结果，
// 只有在"被人拿到密文之后"才会暴露问题，而那时已经太晚。
// 所以这个文件靠本文件末尾那组测试验收，不靠读代码验收。
package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SecretBox 是对称加解密的窄接口。
//
// BYOK 存 API Key 密文时用它，业务层（internal/llm）只认识 Seal/Open，
// 不需要知道 AES、GCM、nonce 这些术语——这是"实现细节收敛在一个接口后面"
// 的又一处应用，和 platform.Querier 收敛 pgx 是同一个道理。
type SecretBox interface {
	// Seal 加密一段明文。
	//
	// 【关键性质】同一段明文调用两次，两次的输出必须不同。
	// 这不是随便的好品味——如果输出总是一样，攻击者不需要破解算法，
	// 只要看到两条密文相同就能推断出两个用户用了同一个 API Key。
	// 靠每次调用换一个随机 nonce 做到，见 aesGCMBox.Seal 的实现。
	Seal(plaintext []byte) ([]byte, error)

	// Open 解密。密钥不对、密文被改过任意一个字节、密文长度不对，
	// 全部必须返回 error，不能返回一段"看起来正常但是错的"明文——
	// AES-GCM 的认证标签就是为了保证这一点，Open 不能把它旁路掉。
	Open(ciphertext []byte) ([]byte, error)
}

// ErrDecryptFailed 是 Open 唯一可能返回的错误。
//
// 密钥错、密文被篡改、密文太短，原因不同，但故意归成一个错误——
// 分别报告"密钥错"还是"被篡改"，等于给攻击者一个可以用来试错的信号
// （这是密码学里 padding oracle 那类攻击的根源）。调用方只需要
// errors.Is(err, ErrDecryptFailed) 就知道"这段解不开"，不需要知道为什么。
var ErrDecryptFailed = errors.New("secretbox: decrypt failed")

// masterKeyBytes 是 AES-256 要求的密钥长度。
const masterKeyBytes = 32

// aesGCMBox 是 SecretBox 唯一的实现：AES-256-GCM。
//
// 选 GCM 不选 CBC 之类的模式：GCM 自带认证（AEAD），解密时能识别密文
// 是否被篡改；CBC 只管保密性，篡改检测要额外拼别的机制，容易漏。
type aesGCMBox struct {
	gcm cipher.AEAD
}

// NewSecretBox 按三级顺序解析主密钥，再用它构造 AEAD。
//
// 三级来源（技术方案 §1 的安全基线）：
//
//	① cfg.MasterKey 非空          → 十六进制解码，直接用
//	② 为空，MasterKeyPath 文件存在 → 读文件里的十六进制字符串
//	③ 文件也不存在                → 随机生成 32 字节，写回那个路径（权限 0600），
//	                                以后每次启动都从这个文件读到同一个密钥
//
// 任何一级解析失败都直接返回 error，不悄悄退到下一级——
// 如果 CONGORAG_MASTER_KEY 设置了但格式不对，那通常是配置失误，
// 悄悄退到"生成一个新的"只会让人以为在用自己配的密钥，实际早就换了一把。
func NewSecretBox(cfg *Config) (SecretBox, error) {
	key, err := resolveMasterKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve master key: %w", err)
	}
	return newAESGCMBox(key)
}

// newAESGCMBox 从一个已经确定长度的密钥构造实现。
//
// 拆成这个函数是为了让测试能绕开 resolveMasterKey 的文件系统 I/O，
// 直接拿一个已知的 key 去测 Seal/Open 的性质。
func newAESGCMBox(key []byte) (SecretBox, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return &aesGCMBox{gcm: gcm}, nil
}

func resolveMasterKey(cfg *Config) ([]byte, error) {
	// ① 环境变量。
	if cfg.MasterKey != "" {
		return decodeMasterKeyHex("CONGORAG_MASTER_KEY", cfg.MasterKey)
	}

	// ② 文件已存在。
	//
	// 【为什么先 Stat 再读】读文件这件事有两种含义完全不同的失败：文件不存在
	// （走③是对的），和文件存在但读不了，比如权限问题（这是配置错误，必须
	// 报出来，不能悄悄当成"不存在"然后生成一把新密钥，覆盖用户原本能用的
	// 那把）。除了这两种，还有一种"存在但此刻是空的"——另一个进程刚把它
	// 创建出来、还没写完，由 readPersistedMasterKey 短暂重试等它写完。
	_, statErr := os.Stat(cfg.MasterKeyPath)
	switch {
	case statErr == nil:
		return readPersistedMasterKey(cfg.MasterKeyPath)
	case os.IsNotExist(statErr):
		// ③ 文件不存在，生成并落盘。
		return generateAndPersistMasterKey(cfg.MasterKeyPath)
	default:
		return nil, fmt.Errorf("stat %s: %w", cfg.MasterKeyPath, statErr)
	}
}

func decodeMasterKeyHex(source, hexStr string) ([]byte, error) {
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid hex: %w", source, err)
	}
	if len(key) != masterKeyBytes {
		return nil, fmt.Errorf("%s must decode to %d bytes, got %d", source, masterKeyBytes, len(key))
	}
	return key, nil
}

// generateAndPersistMasterKey 在文件不存在时铸造一把密钥并落盘。
//
// 【为什么不能直接 os.WriteFile】O_CREATE|O_TRUNC 既不独占也不原子：
// 两个进程（文档里写明的 make dev 与 make dev-worker，或者 api/worker
// 两个容器）同时启动、同时发现文件不存在时，各自铸造一把 32 字节的密钥，
// 后写者截断并覆盖先写者——两边都拿到一把"能用"的 SecretBox，但只有最后
// 写入者的密钥留在磁盘上。落败方用它封的密文（BYOK 的 provider API Key）
// 在下次重启后永远解不开，且不可恢复。同一非原子性还有第二种表现：
// 撞进"文件已创建、还没写完"窗口的读者读到 0 字节，
// decodeMasterKeyHex 报 must decode to 32 bytes, got 0，进程直接起不来。
//
// 【两步：先写临时文件，再原子发布】
//
//	① 密钥先写进同目录下的临时文件（0600，写完 fsync 再 close）——
//	   目标路径上永远不会出现半截内容，读者要么看不到它，要么看到完整的
//	   64 个十六进制字符；
//	② 用 os.Link 把临时文件发布成目标文件：link 在目标已存在时失败
//	   （EEXIST）而不会覆盖，所以"谁赢"是排他的；落败方回读赢家已经落盘的
//	   密钥，绝不返回一把不在磁盘上的密钥。
func generateAndPersistMasterKey(path string) ([]byte, error) {
	key := make([]byte, masterKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create master key directory: %w", err)
	}

	tmp, err := writeMasterKeyTempFile(path, key)
	if err != nil {
		return nil, err
	}
	// 发布成功后这个删除只是去掉临时文件名（同一份数据还挂在目标文件名下）；
	// 失败路径上它负责收尾，不留垃圾。
	defer os.Remove(tmp)

	if err := os.Link(tmp, path); err != nil {
		if os.IsExist(err) {
			// 别的进程先发布了：作废自己那把，回读磁盘上真正生效的那把。
			return readPersistedMasterKey(path)
		}
		// 硬链接不可用的文件系统上的退路：用 O_EXCL 独占创建目标文件。
		// 这条路有"文件已创建、还没写完"的窗口，上面那个重试读法负责
		// 抹平它——所以退路不会退化成"读到 0 字节就崩"。
		return claimMasterKeyPath(path, key)
	}
	return key, nil
}

// writeMasterKeyTempFile 把密钥写进 path 同目录下的临时文件，返回它的路径。
// 同目录是必需的：link/rename 只在同一个文件系统内才是原子操作。
func writeMasterKeyTempFile(path string, key []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp master key next to %s: %w", path, err)
	}
	// 权限 0600：只有当前用户能读写。os.CreateTemp 就是以 0600 创建临时文件
	// 的，最终文件是它的硬链接，所以这个权限一直带到底，不用再改。
	//
	// 【平台差异,不是本文件的 bug】Go 在 Windows 上不模拟 POSIX 权限位——
	// 这里请求的 0600 在 Windows 上会被折叠成别的值，测试文件里那条
	// 权限断言按 runtime.GOOS 分支处理，就是为了不让这个已知的平台差异
	// 冒充"代码写错了"。生产部署目标是 Linux 容器，那里 0600 是真的生效。
	_, werr := f.WriteString(hex.EncodeToString(key))
	if werr == nil {
		// fsync：内容必须真的落到介质上再发布，否则掉电后目标文件可能又是空的。
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write temp master key for %s: %w", path, werr)
	}
	return f.Name(), nil
}

// claimMasterKeyPath 是硬链接不可用时的退路：直接以 O_EXCL 独占创建目标
// 文件——同样只有一个进程能赢，输的那个回读。
func claimMasterKeyPath(path string, key []byte) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return readPersistedMasterKey(path)
		}
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	_, werr := f.WriteString(hex.EncodeToString(key))
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		// 半截文件比没有文件更糟：别的进程会把它当成"密钥就在这儿"。
		// 删掉，让下一次启动重新走一遍。
		os.Remove(path)
		return nil, fmt.Errorf("write %s: %w", path, werr)
	}
	return key, nil
}

// readPersistedMasterKey 读磁盘上此刻真正落下的那把密钥。
//
// 【为什么带重试】空文件只可能出现在"另一个进程刚创建了它、还没写完"这个
// 窗口里（走 claimMasterKeyPath 那条退路时）。立刻按内容报错会让一个无辜
// 进程直接起不来，所以要等一小会儿。等满仍为空就交给 decodeMasterKeyHex
// 报它本来那句 must decode to 32 bytes, got 0——错误信息不变，只是不再
// 因为一个几十微秒的窗口而误报。
func readPersistedMasterKey(path string) ([]byte, error) {
	const (
		attempts = 50
		interval = 10 * time.Millisecond
	)
	for i := 0; ; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		text := strings.TrimSpace(string(data))
		if text != "" || i == attempts-1 {
			return decodeMasterKeyHex(path, text)
		}
		time.Sleep(interval)
	}
}

func (b *aesGCMBox) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// nonce 拼在密文前面一起存，Open 时从头部切出来，不需要单独一列存它。
	// "同一段明文两次加密结果不同"全靠这个随机 nonce——AES-GCM 本身
	// 对同样的 (key, nonce, plaintext) 是确定性的，nonce 变了输出才变。
	sealed := b.gcm.Seal(nonce, nonce, plaintext, nil)
	return sealed, nil
}

func (b *aesGCMBox) Open(ciphertext []byte) ([]byte, error) {
	nonceSize := b.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrDecryptFailed
	}

	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := b.gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		// gcm.Open 的原始错误不带任何细节（故意设计成这样，防旁路攻击）。
		// 统一翻译成 ErrDecryptFailed，调用方用 errors.Is 判断，不看错误文案。
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}
