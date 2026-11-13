package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 配置读错了大多不报错，只是行为悄悄变了——
// 监听地址变成 0.0.0.0 就是对局域网敞开，而服务照样起得来。

func TestLoadConfig_RequiresDatabaseURL(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "")

	cfg, err := LoadConfig()

	require.Error(t, err, "没有数据库地址时必须拒绝启动，不能带着空配置跑起来")
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "CONGORAG_DB_URL", "报错要指名是哪个变量")
}

func TestLoadConfig_DefaultsWhenOnlyDBURLSet(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	for _, k := range []string{
		"CONGORAG_LISTEN_ADDR",
		"CONGORAG_DOCUMENTS_DIR", "CONGORAG_LOG_LEVEL", "CONGORAG_MASTER_KEY",
		"CONGORAG_MASTER_KEY_PATH",
	} {
		t.Setenv(k, "")
	}

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/congorag", cfg.DatabaseURL)
	assert.Equal(t, "127.0.0.1:3210", cfg.ListenAddr)
	assert.Equal(t, "./data/documents", cfg.DocumentsDir)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Empty(t, cfg.MasterKey)
	assert.Equal(t, "./data/master.key", cfg.MasterKeyPath)
}

// 【这一条是安全断言，不是默认值断言】
//
// 默认监听地址必须是回环。改成 0.0.0.0 的话，端口对整个局域网打开——
// 服务照常启动、测试照常通过、界面照常能用，没有任何一处会报错。
// 方案 §1 的部署边界是"本地单机应用"，这条守的就是那句话。
//
// 容器里需要 0.0.0.0 时由 compose 设 CONGORAG_LISTEN_ADDR 覆盖，
// 那是显式的选择，和"默认就敞开"不是一回事。
func TestLoadConfig_DefaultListenAddrIsLoopback(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	t.Setenv("CONGORAG_LISTEN_ADDR", "")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:3210", cfg.ListenAddr,
		"默认不能绑 0.0.0.0——那会把服务暴露到局域网，且不会有任何报错")
}

func TestLoadConfig_EnvOverridesDefaults(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://db/x")
	t.Setenv("CONGORAG_LISTEN_ADDR", "0.0.0.0:3210")
	t.Setenv("CONGORAG_DOCUMENTS_DIR", "/var/data")
	t.Setenv("CONGORAG_LOG_LEVEL", "debug")
	t.Setenv("CONGORAG_MASTER_KEY", "deadbeef")
	t.Setenv("CONGORAG_MASTER_KEY_PATH", "/etc/congorag/master.key")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:3210", cfg.ListenAddr)
	assert.Equal(t, "/var/data", cfg.DocumentsDir)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "deadbeef", cfg.MasterKey)
	assert.Equal(t, "/etc/congorag/master.key", cfg.MasterKeyPath)
}

// 【这一条钉的是部署事实，不是默认值】CONGORAG_PORT 不归这个进程读。
//
// 它在交付期 compose 里是**宿主机**那一侧的端口：
// deployments/startup/docker-compose.yml 写的是
// "127.0.0.1:${CONGORAG_PORT:-3210}:3210"——容器内永远监听 3210，
// CONGORAG_PORT 只决定宿主机哪个端口转发进去。
//
// 如果哪天有人"顺手"把它接进 ListenAddr 让它看起来生效，容器里就会去
// 监听那个宿主机端口，而 compose 仍然把 ${CONGORAG_PORT} 映到容器的
// 3210：容器正常启动、日志照常打印 listening、健康检查也过得去，
// 只有从外面连不进来。删掉这条断言的话，这个坏法没有任何一处会报错。
func TestLoadConfig_PortEnvVarDoesNotChangeListenAddr(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	t.Setenv("CONGORAG_LISTEN_ADDR", "")
	t.Setenv("CONGORAG_PORT", "8080")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, defaultListenAddr, cfg.ListenAddr,
		"CONGORAG_PORT 是 compose 的宿主机端口，接进监听地址会让容器场景静默失联")
}

// 只有空格的环境变量等于没设。
// 不 trim 的话，配置文件里多打一个空格会让监听地址变成 " 3210" 这种东西，
// 启动时才报一个看不懂的错。
func TestEnvOr_WhitespaceCountsAsUnset(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	t.Setenv("CONGORAG_LOG_LEVEL", "   ")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "info", cfg.LogLevel)
}

func TestEnvOr(t *testing.T) {
	t.Setenv("CONGORAG_TEST_KEY", "value")
	assert.Equal(t, "value", envOr("CONGORAG_TEST_KEY", "fallback"))

	t.Setenv("CONGORAG_TEST_KEY", "")
	assert.Equal(t, "fallback", envOr("CONGORAG_TEST_KEY", "fallback"))

	assert.Equal(t, "fallback", envOr("CONGORAG_DEFINITELY_NOT_SET", "fallback"))
}

// 上传上限只有"被读成了 0"这一个危险方向——0 在调用侧的约定是"不限"，
// 一个手滑打进去的 0 会让整条防线无声消失，而配置解析当时不报任何错。
// 所以非法值一律退回默认值，测试要盯住的正是这一条。
func TestLoadConfig_MaxUploadBytes(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")

	for _, tt := range []struct {
		name string
		env  string
		want int64
	}{
		{"没设就用默认值", "", defaultMaxUploadBytes},
		{"显式覆盖", "1048576", 1048576},
		{"多了空格也算数", "  2048  ", 2048},
		{"不是数字退回默认值", "32MB", defaultMaxUploadBytes},
		{"0 退回默认值", "0", defaultMaxUploadBytes},
		{"负数退回默认值", "-1", defaultMaxUploadBytes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CONGORAG_MAX_UPLOAD_BYTES", tt.env)

			cfg, err := LoadConfig()

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.MaxUploadBytes)
			assert.Positive(t, cfg.MaxUploadBytes, "上限为 0 等于把限关掉，配置解析必须把它挡回来")
		})
	}
}

// ────────────────────────────────────────────────────────────────
// 限流配置
// ────────────────────────────────────────────────────────────────

// 四个 CONGORAG_RATE_LIMIT_* 的默认值就是对外承诺的额度，和代码里的
// 常量分开断言：常量被"顺手调小"时这条会红，提醒改的人这是行为变更。
func TestLoadConfig_RateLimitDefaults(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	clearRateLimitEnv(t)

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, 240, cfg.RateLimitPerMinute, "general 档默认 240 令牌/分钟")
	assert.Equal(t, 60, cfg.RateLimitBurst, "general 档 burst 默认 60")
	assert.Equal(t, 30, cfg.RateLimitLLMPerMinute, "expensive 档默认 30 令牌/分钟")
	assert.Equal(t, 8, cfg.RateLimitLLMBurst, "expensive 档 burst 默认 8")
}

func TestLoadConfig_RateLimitOverrides(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	t.Setenv("CONGORAG_RATE_LIMIT_PER_MINUTE", "120")
	t.Setenv("CONGORAG_RATE_LIMIT_BURST", " 30 ")
	t.Setenv("CONGORAG_RATE_LIMIT_LLM_PER_MINUTE", "0")
	t.Setenv("CONGORAG_RATE_LIMIT_LLM_BURST", "0")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, 120, cfg.RateLimitPerMinute, "数字两侧空白应被接受")
	assert.Equal(t, 30, cfg.RateLimitBurst)
	assert.Equal(t, 0, cfg.RateLimitLLMPerMinute, "0 表示关闭这一档，是合法值")
	assert.Equal(t, 0, cfg.RateLimitLLMBurst)
}

// 负数和解析不出数字的值必须让启动失败，而不是静默回退默认值：
// 回退等于把用户写错的额度换成一个他没写过的数字，且没有任何提示。
func TestLoadConfig_RateLimitRejectsInvalidValues(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")

	for _, tt := range []struct {
		name string
		key  string
		val  string
	}{
		{"general 速率为负", "CONGORAG_RATE_LIMIT_PER_MINUTE", "-1"},
		{"general burst 为负", "CONGORAG_RATE_LIMIT_BURST", "-5"},
		{"expensive 速率为负", "CONGORAG_RATE_LIMIT_LLM_PER_MINUTE", "-30"},
		{"expensive burst 为负", "CONGORAG_RATE_LIMIT_LLM_BURST", "-8"},
		{"不是整数", "CONGORAG_RATE_LIMIT_PER_MINUTE", "240/min"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearRateLimitEnv(t)
			t.Setenv(tt.key, tt.val)

			cfg, err := LoadConfig()

			require.Error(t, err, "非法限流配置必须拒绝启动")
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.key, "报错要指名是哪个变量")
		})
	}
}

// burst=0 与 rate>0 是自相矛盾的配置：桶永远拿不到令牌，表现成该端点全被拒。
// 0 只应作为"关闭这一档"与 rate 一起出现。
func TestLoadConfig_RateLimitRejectsEnabledTierWithoutBurst(t *testing.T) {
	t.Setenv("CONGORAG_DB_URL", "postgres://localhost/congorag")
	clearRateLimitEnv(t)
	t.Setenv("CONGORAG_RATE_LIMIT_BURST", "0")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CONGORAG_RATE_LIMIT_BURST")

	// 两档都显式关闭时，burst 为 0 是合法的（停止使用该配置）。
	clearRateLimitEnv(t)
	t.Setenv("CONGORAG_RATE_LIMIT_PER_MINUTE", "0")
	t.Setenv("CONGORAG_RATE_LIMIT_BURST", "0")
	t.Setenv("CONGORAG_RATE_LIMIT_LLM_PER_MINUTE", "0")
	t.Setenv("CONGORAG_RATE_LIMIT_LLM_BURST", "0")

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Zero(t, cfg.RateLimitPerMinute)
	assert.Zero(t, cfg.RateLimitLLMPerMinute)
}

// clearRateLimitEnv 把四个限流变量恢复成"没设置"，避免开发者本机环境
// 泄漏进默认值断言。
func clearRateLimitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONGORAG_RATE_LIMIT_PER_MINUTE",
		"CONGORAG_RATE_LIMIT_BURST",
		"CONGORAG_RATE_LIMIT_LLM_PER_MINUTE",
		"CONGORAG_RATE_LIMIT_LLM_BURST",
	} {
		t.Setenv(key, "")
	}
}
