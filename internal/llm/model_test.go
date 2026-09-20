package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Capabilities <-> text[] 的转换往返必须是恒等的——
// 这一对函数是 llm_models.capabilities 列唯一的读写入口，
// 存的时候漏一个能力位或读的时候认错一个字符串都不会报错，
// 只会让"这个模型支不支持 tool calling"这类判断悄悄错。
func TestCapabilities_ToSlice_FromSlice_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		caps Capabilities
	}{
		{"全部为假", Capabilities{}},
		{"全部为真", Capabilities{Chat: true, Streaming: true, ToolCalling: true, Embedding: true, Reasoning: true}},
		{"只有 chat", Capabilities{Chat: true}},
		{"只有 embedding", Capabilities{Embedding: true}},
		{"chat+streaming+tool_calling,典型的聊天模型", Capabilities{Chat: true, Streaming: true, ToolCalling: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slice := tt.caps.toSlice()
			got := capabilitiesFromSlice(slice)
			assert.Equal(t, tt.caps, got)
		})
	}
}

// 认不出的字符串直接跳过,不报错——见 capabilitiesFromSlice 的注释：
// 这是故意的前向兼容行为，不是遗漏的 case。
func TestCapabilitiesFromSlice_UnknownNamesAreIgnored(t *testing.T) {
	got := capabilitiesFromSlice([]string{"chat", "some_future_capability", "embedding"})
	assert.Equal(t, Capabilities{Chat: true, Embedding: true}, got)
}

func TestCapabilitiesFromSlice_Empty(t *testing.T) {
	assert.Equal(t, Capabilities{}, capabilitiesFromSlice(nil))
	assert.Equal(t, Capabilities{}, capabilitiesFromSlice([]string{}))
}
