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
		"CONGORAG_LISTEN_ADDR", "CONGORAG_PORT",
		"CONGORAG_DOCUMENTS_DIR", "CONGORAG_LOG_LEVEL", "CONGORAG_MASTER_KEY",
		"CONGORAG_MASTER_KEY_PATH",
	} {
		t.Setenv(k, "")
	}

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/congorag", cfg.DatabaseURL)
	assert.Equal(t, "127.0.0.1:3210", cfg.ListenAddr)
	assert.Equal(t, "3210", cfg.Port)
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
	t.Setenv("CONGORAG_PORT", "8080")
	t.Setenv("CONGORAG_DOCUMENTS_DIR", "/var/data")
	t.Setenv("CONGORAG_LOG_LEVEL", "debug")
	t.Setenv("CONGORAG_MASTER_KEY", "deadbeef")
	t.Setenv("CONGORAG_MASTER_KEY_PATH", "/etc/congorag/master.key")

	cfg, err := LoadConfig()

	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:3210", cfg.ListenAddr)
	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, "/var/data", cfg.DocumentsDir)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "deadbeef", cfg.MasterKey)
	assert.Equal(t, "/etc/congorag/master.key", cfg.MasterKeyPath)
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
