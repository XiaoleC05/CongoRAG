package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveCallConfig 守住"调用方传的温度/上限真的被解析出来"。
//
// 【为什么值得单测】v6.3.0 之前这两个选项被适配器 _ = opts 丢弃：
// 调用方以为翻译温度是 0.1，实际模型收到默认值。单测钉住解析层，
// toEinoOptions 的映射层再由它保证输入非空。
func TestResolveCallConfig(t *testing.T) {
	cfg := resolveCallConfig()
	require.Nil(t, cfg.Temperature)
	require.Nil(t, cfg.MaxTokens)

	cfg = resolveCallConfig(WithTemperature(0.1), WithMaxTokens(2048))
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.1, *cfg.Temperature, 1e-6)
	require.NotNil(t, cfg.MaxTokens)
	require.Equal(t, 2048, *cfg.MaxTokens)

	// 后者覆盖前者：Functional Options 的顺序语义。
	cfg = resolveCallConfig(WithTemperature(0.9), WithTemperature(0.2))
	require.InDelta(t, 0.2, *cfg.Temperature, 1e-6)
}

func TestToEinoOptions_Count(t *testing.T) {
	require.Empty(t, toEinoOptions())
	require.Len(t, toEinoOptions(WithTemperature(0.1)), 1)
	require.Len(t, toEinoOptions(WithMaxTokens(512)), 1)
	require.Len(t, toEinoOptions(WithTemperature(0.1), WithMaxTokens(512)), 2)
}
