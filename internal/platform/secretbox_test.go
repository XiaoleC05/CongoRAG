package platform

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这个文件测的都是"写错了不报错"的性质。
// 每条测试对应 helperDoc 里点名的一种失败模式，缺一条就等于没验收。

// newTestBox 用一个固定的随机密钥构造一个 box，绕开 resolveMasterKey 的
// 文件系统 I/O——这里只测 Seal/Open 本身的密码学性质。
func newTestBox(t *testing.T) SecretBox {
	t.Helper()
	key := make([]byte, masterKeyBytes)
	_, err := rand.Read(key)
	require.NoError(t, err)
	box, err := newAESGCMBox(key)
	require.NoError(t, err)
	return box
}

// 基本往返：加密再解密，必须拿回原文。这是其余测试的前提——
// 如果连这条都不过，后面的失败测试就没有意义。
func TestSecretBox_SealOpen_RoundTrip(t *testing.T) {
	box := newTestBox(t)
	plaintext := []byte("sk-test-api-key-1234567890")

	ciphertext, err := box.Seal(plaintext)
	require.NoError(t, err)

	got, err := box.Open(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// 测试 1（清单第一条）：同一段明文加密两次，密文必须不同。
//
// 这条抓的是 nonce 复用——如果 Seal 每次用固定 nonce 或从计数器取,
// 相同输入会产生相同密文，攻击者不用破解算法就能看出"这两个用户
// 用了同一个 Key"。加密算法本身正确、测试却会一直绿,直到这里加上
// 这条断言。
func TestSecretBox_Seal_NondeterministicAcrossCalls(t *testing.T) {
	box := newTestBox(t)
	plaintext := []byte("sk-same-key-every-time")

	const n = 20
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		ciphertext, err := box.Seal(plaintext)
		require.NoError(t, err)
		key := string(ciphertext)
		assert.False(t, seen[key], "第 %d 次加密的密文和之前某一次重复了——nonce 可能被复用了", i)
		seen[key] = true
	}
}

// 测试 2（清单第二条）：用错误的主密钥解密，必须返回 error，不能是垃圾明文。
//
// 两个不同密钥构造出的 aesGCMBox 之间没有任何关系——用 B 的密钥去解
// A 加密的密文，GCM 的认证标签校验不通过，必须报错。
func TestSecretBox_Open_WrongKeyFails(t *testing.T) {
	boxA := newTestBox(t)
	boxB := newTestBox(t)

	ciphertext, err := boxA.Seal([]byte("secret"))
	require.NoError(t, err)

	_, err = boxB.Open(ciphertext)
	require.Error(t, err, "用另一把密钥必须解不开，不能返回看起来正常的垃圾数据")
	assert.ErrorIs(t, err, ErrDecryptFailed)
}

// 测试 3（清单第三条）：密文改一个字节，解密必须失败。
//
// 这条验证的是 GCM 的认证标签真的在起作用——如果 Open 只解密不校验
// 标签（比如错误地用了不校验完整性的模式），篡改后的密文会"解密"出
// 一段乱码但不报错，调用方拿到的是被动过的数据却以为是合法的。
func TestSecretBox_Open_TamperedCiphertextFails(t *testing.T) {
	box := newTestBox(t)
	ciphertext, err := box.Seal([]byte("do not tamper with me"))
	require.NoError(t, err)

	// 翻转密文体里最后一个字节的最低位。改 nonce 部分也会失败，
	// 但改密文体更直接地验证"认证标签覆盖了密文本身"。
	tampered := append([]byte{}, ciphertext...)
	tampered[len(tampered)-1] ^= 0x01

	_, err = box.Open(tampered)
	require.Error(t, err, "改动过的密文必须解不开")
	assert.ErrorIs(t, err, ErrDecryptFailed)
}

// 密文太短（比如被截断到比 nonce 还短）也必须报错，不能 panic 或越界。
func TestSecretBox_Open_TooShortFails(t *testing.T) {
	box := newTestBox(t)
	_, err := box.Open([]byte{1, 2, 3})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDecryptFailed)
}

// 空明文是合法输入（比如一个空 API Key，虽然业务层大概会在更早的地方拒绝，
// 但 secretbox 自己不应该假设"明文一定非空"）。
func TestSecretBox_SealOpen_EmptyPlaintext(t *testing.T) {
	box := newTestBox(t)
	ciphertext, err := box.Seal([]byte{})
	require.NoError(t, err)

	got, err := box.Open(ciphertext)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ────────────────────────────────────────────────────────────────
// resolveMasterKey 的三级来源
// ────────────────────────────────────────────────────────────────

// ① 环境变量非空时，优先用它，即使文件路径上还有另一把密钥。
// 这条防的是"改错了优先级"——如果实现里两个 if 顺序写反，
// 用户显式配置的 CONGORAG_MASTER_KEY 会被文件里的旧密钥悄悄覆盖。
func TestResolveMasterKey_EnvTakesPriorityOverFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	fileKey := make([]byte, masterKeyBytes)
	_, err := rand.Read(fileKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(fileKey)), 0o600))

	envKey := make([]byte, masterKeyBytes)
	_, err = rand.Read(envKey)
	require.NoError(t, err)

	cfg := &Config{MasterKey: hex.EncodeToString(envKey), MasterKeyPath: path}
	got, err := resolveMasterKey(cfg)
	require.NoError(t, err)
	assert.Equal(t, envKey, got, "环境变量存在时必须用它，不能被文件路径上的旧密钥抢先")
}

// ② 文件存在且环境变量为空时，读文件里的密钥。
func TestResolveMasterKey_ReadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	fileKey := make([]byte, masterKeyBytes)
	_, err := rand.Read(fileKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(fileKey)), 0o600))

	cfg := &Config{MasterKeyPath: path}
	got, err := resolveMasterKey(cfg)
	require.NoError(t, err)
	assert.Equal(t, fileKey, got)
}

// ③ 文件不存在时自动生成，且两次启动（两次调用 resolveMasterKey）
// 读到的是同一把密钥——这是"密钥固定下来"这句话的可执行验证：
// 如果每次都重新生成，之前加密的所有 API Key 全部变得解不开。
func TestResolveMasterKey_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "master.key") // 故意嵌一层，顺带测 MkdirAll

	cfg := &Config{MasterKeyPath: path}

	first, err := resolveMasterKey(cfg)
	require.NoError(t, err)
	assert.Len(t, first, masterKeyBytes)

	second, err := resolveMasterKey(cfg)
	require.NoError(t, err)
	assert.Equal(t, first, second, "第二次必须读到同一把密钥，不能每次启动都生成新的")
}

// ③ 的并发版本：全新安装（没有 data/master.key）时两个进程同时启动——
// 文档里写明的 `make dev` 和 `make dev-worker` 各占一个终端，或者 api 与
// worker 两个容器同时起。同一个路径上只能有一把密钥，而且每个调用拿到的
// 必须是磁盘上那把。
//
// 【修复前这条一定失败】原实现是每个调用各 rand.Read 一把、再用
// O_CREATE|O_TRUNC 覆盖写：8 个调用会返回 8 把不同的密钥，只有最后写入者
// 那把留在磁盘上，落败方保留一把不落任何介质的密钥——它封的密文（BYOK 的
// provider API Key）重启后永远解不开，用户只能重填。
func TestResolveMasterKey_ConcurrentGenerationConvergesToOneKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	cfg := &Config{MasterKeyPath: path}

	const n = 8
	keys := make([][]byte, n)
	errs := make([]error, n)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 尽量让它们真的同时冲进去
			keys[i], errs[i] = resolveMasterKey(cfg)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "第 %d 个进程解析主密钥失败", i)
		require.Len(t, keys[i], masterKeyBytes)
		assert.Equal(t, keys[0], keys[i],
			"第 %d 个进程拿到的密钥和第一个不一样——落败方会持有不落磁盘的密钥", i)
	}

	// 磁盘上那把必须就是所有人拿到的那把（"绝不返回不在磁盘上的密钥"）。
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(keys[0]), strings.TrimSpace(string(onDisk)))

	// 临时文件一个都不该留下。
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "master.key", entries[0].Name())
}

// 落在"文件已创建、内容还没写完"窗口里的读者必须等它写完，而不是把空文件
// 当成损坏的密钥直接返回错误——修复前这条读法会报
// must decode to 32 bytes, got 0，进程启动即失败。
func TestReadPersistedMasterKey_WaitsForInFlightWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	want := make([]byte, masterKeyBytes)
	_, err := rand.Read(want)
	require.NoError(t, err)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, []byte(hex.EncodeToString(want)), 0o600)
	}()

	got, err := readPersistedMasterKey(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// 但等待要有上限：一个真正空着（或写了一半就崩了）的 master.key 不能把
// 启动流程挂死，也不能悄悄生成一把新的——必须报出"这个文件不是一把密钥"。
func TestReadPersistedMasterKey_EmptyFileFailsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	start := time.Now()
	_, err := readPersistedMasterKey(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must decode to 32 bytes, got 0")
	assert.Less(t, time.Since(start), 5*time.Second, "空文件不能无限等下去")
}

// 测试 4（清单第四条）：自动生成的 master.key 权限必须是 0600。
//
// 【平台差异见 secretbox.go 顶部的注释】Windows 不模拟 POSIX 权限位，
// os.WriteFile 请求的 0600 在这台机器上不会真的生效——这不是代码的 bug,
// 是这台开发机的平台限制。生产部署目标是 Linux 容器,这条断言在那里才
// 有意义，所以严格校验只在非 Windows 上跑；Windows 上退化成"文件确实
// 被创建了"这条弱得多的检查，避免用一个平台差异冒充代码错误。
func TestResolveMasterKey_GeneratedFilePermissionIs0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	cfg := &Config{MasterKeyPath: path}

	_, err := resolveMasterKey(cfg)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)

	if runtime.GOOS == "windows" {
		t.Skip("Windows 不模拟 POSIX 权限位,这条断言只在 Linux/macOS 上有意义,见本文件顶部注释")
	}
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"master.key 必须只有当前用户可读写,不能是 0644 之类更宽的权限")
}

// 十六进制格式不对时必须报错，不能悄悄截断或用零填充。
func TestResolveMasterKey_InvalidHexFails(t *testing.T) {
	cfg := &Config{MasterKey: "not-hex-at-all"}
	_, err := resolveMasterKey(cfg)
	require.Error(t, err)
}

// 长度不对（比如给了一个 16 字节的 AES-128 密钥）必须报错，
// 不能默默截断或补零凑成 32 字节——那样密钥的实际强度和用户以为的不一致。
func TestResolveMasterKey_WrongLengthFails(t *testing.T) {
	shortKey := make([]byte, 16)
	_, err := rand.Read(shortKey)
	require.NoError(t, err)

	cfg := &Config{MasterKey: hex.EncodeToString(shortKey)}
	_, err = resolveMasterKey(cfg)
	require.Error(t, err)
}
