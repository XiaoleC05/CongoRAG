package platform

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SSE 的 error frame 和 REST 的 Problem.type 是同一套枚举
// （docs/sse-protocol.md），两边都走这一个函数——映射错了不会编译失败，
// 只会让前端把"参数不合法"显示成"服务内部错误"，所以要有测试兜住。
func TestSSEErrorType(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"参数不合法", ErrInvalid, "invalid_argument"},
		{"资源不存在", ErrNotFound, "not_found"},
		{"上游模型失败", ErrUpstream, "upstream_llm_error"},
		{"包装过的 sentinel", errors.Join(errors.New("start agent run"), ErrNotFound), "not_found"},
		{"认不出来的一律 internal_error", errors.New("boom"), "internal_error"},
		{"nil", nil, "internal_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SSEErrorType(tc.err))
		})
	}
}
