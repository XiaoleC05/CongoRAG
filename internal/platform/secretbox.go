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
	// os.ReadFile 的 err 有两种含义完全不同的情况：文件不存在（走③是对的），
	// 和文件存在但读不了，比如权限问题（这是配置错误，必须报出来，
	// 不能悄悄当成"不存在"然后生成一把新密钥，覆盖用户原本能用的那把）。
	data, err := os.ReadFile(cfg.MasterKeyPath)
	if err == nil {
		return decodeMasterKeyHex(cfg.MasterKeyPath, strings.TrimSpace(string(data)))
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", cfg.MasterKeyPath, err)
	}

	// ③ 文件不存在，生成并落盘。
	return generateAndPersistMasterKey(cfg.MasterKeyPath)
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

func generateAndPersistMasterKey(path string) ([]byte, error) {
	key := make([]byte, masterKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create master key directory: %w", err)
	}

	// 权限 0600：只有当前用户能读写。
	//
	// 【平台差异,不是本文件的 bug】Go 在 Windows 上不模拟 POSIX 权限位——
	// 这里请求的 0600 在 Windows 上会被折叠成别的值，测试文件里那条
	// 权限断言按 runtime.GOOS 分支处理，就是为了不让这个已知的平台差异
	// 冒充"代码写错了"。生产部署目标是 Linux 容器，那里 0600 是真的生效。
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}

	return key, nil
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
