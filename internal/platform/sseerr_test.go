package platform

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SSE 的 error frame 和 REST 的 Problem.type 是同一套枚举
// （docs/sse-protocol.md），两边都从 Classify 那一张表取值——枚举本身
// 只有一个定义，这一列只是它的镜像。
//
// 【为什么还要逐条钉】"共用同一张表"这件事没有编译期约束：SSEErrorType
// 完全可以再自己 switch 一遍（它以前就是），而缺档位不会报错——同一个
// ErrConflict 会在实时帧里报 conflict、在断线重放里报 internal_error，
// 用户重连后看到的第一句话和当时看到的对不上（issue #112）。
// 这张表就是那句"两边必须一致"的断言。
func TestClassify_TypeAndStatusPerSentinel(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantType   string
		wantStatus int
	}{
		{"参数不合法", ErrInvalid, "invalid_argument", http.StatusBadRequest},
		{"资源不存在", ErrNotFound, "not_found", http.StatusNotFound},
		{"重复键", ErrDuplicateKey, "conflict_duplicate_key", http.StatusConflict},
		{"状态冲突", ErrConflict, "conflict", http.StatusConflict},
		{"外键指向的行不存在", ErrForeignKey, "not_found", http.StatusNotFound},
		{"上游模型失败", ErrUpstream, "upstream_llm_error", http.StatusBadGateway},
		{"快照版本不兼容", ErrStateSchemaVersionMismatch, "state_schema_version_mismatch", http.StatusConflict},
		{"副作用已生效", ErrToolEffectApplied, "tool_effect_already_applied", http.StatusConflict},
		{"不允许重放", ErrReplayUnsafe, "replay_unsafe", http.StatusConflict},
		{"包装过的 sentinel", errors.Join(errors.New("start agent run"), ErrNotFound), "not_found", http.StatusNotFound},
		{"认不出来的一律 internal_error", errors.New("boom"), "internal_error", http.StatusInternalServerError},
		{"nil", nil, "internal_error", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, typ, _ := Classify(tc.err)

			assert.Equal(t, tc.wantType, typ)
			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, typ, SSEErrorType(tc.err),
				"SSE 帧的 type 必须和 REST 的 Problem.type 是同一个值")
		})
	}
}

// TestSSEErrorType_TypeEnumMatchesProtocolDoc 把同一个错误的三处取值钉在一起：
// 这张表（硬编码，不现取）、Classify 的 type/status 列、SSEErrorType 的返回值。
//
// 【为什么上面那条断言还不够】上一个测试是拿 Classify 的返回值去比
// SSEErrorType——两边一起改错也照样相等。而 sentinel.go 的注释警告的是另一种
// 失败："新增 sentinel 忘了更新表不会编译失败，只会静默落进 internal_error"。
// 那条只有拿**写死的期望值**去比才查得出来：ErrInvalid 必须是 invalid_argument，
// 不能只是"和 SSE 那侧一样"。
//
// 【为什么期望值要照抄 web/README.md 那张表】type 是前端按它选文案的协议枚举
// （web/src/lib/errors.ts 的 MESSAGES），那张表就是它的对外文档。测试里写死
// 这些值，改错了这里会红，而不是等到前端发现"这个 type 没有对应文案"。
//
// 【为什么 ErrIdempotentHit 期望 internal_error】它刻意不在 Classify 的表里
// （sentinel.go 写明：它是"命中幂等键"这个正常路径的信号，会被 usecase 转成
// "把上一轮的结果还给他"）。所以它落到 default 一档是预期行为，这一行把它
// 钉住——哪天有人顺手给它在表里加一档，这里会红。
//
// 【不在这张表里的两档】context_overflow（400）与
// embedding_change_requires_reindex（409）不在 platform 的表里：那两个 sentinel
// 属于 ctxmgr / llm，platform 反向 import 它们会成环，所以由调用方（apps/api 的
// classify、internal/conversation 的 eventErrorClass）先判。它们必须排在
// platform.Classify 之前，否则会落进 default 被当成 500。
func TestSSEErrorType_TypeEnumMatchesProtocolDoc(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantType   string
		wantStatus int
	}{
		{"参数不合法", ErrInvalid, "invalid_argument", http.StatusBadRequest},
		{"资源不存在", ErrNotFound, "not_found", http.StatusNotFound},
		{"引用的父行不存在", ErrForeignKey, "not_found", http.StatusNotFound},
		{"重复键", ErrDuplicateKey, "conflict_duplicate_key", http.StatusConflict},
		{"状态冲突", ErrConflict, "conflict", http.StatusConflict},
		{"快照版本不兼容", ErrStateSchemaVersionMismatch, "state_schema_version_mismatch", http.StatusConflict},
		{"副作用已生效", ErrToolEffectApplied, "tool_effect_already_applied", http.StatusConflict},
		{"不允许重放", ErrReplayUnsafe, "replay_unsafe", http.StatusConflict},
		{"上游模型失败", ErrUpstream, "upstream_llm_error", http.StatusBadGateway},
		// 刻意不在表里的一档，见上面注释。
		{"幂等命中（不出现在映射表里）", ErrIdempotentHit, "internal_error", http.StatusInternalServerError},
		{"认不出来的一律 internal_error", errors.New("boom"), "internal_error", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, typ, _ := Classify(tc.err)
			assert.Equal(t, tc.wantType, typ, "Classify 的 type 必须和 web/README.md 那张枚举表一致")
			assert.Equal(t, tc.wantStatus, status, "Classify 的 status 必须和那张表一致")
			// SSE 帧没有状态码的位置，但 type 必须和 REST 同值——这里比的是
			// 写死的期望值，所以 SSEErrorType 自己 switch 一遍（缺档位）也会红。
			assert.Equal(t, tc.wantType, SSEErrorType(tc.err),
				"SSE 帧的 type 必须和 REST 的 Problem.type 是同一个值")
		})
	}
}

// SafeDetail 只管一件事：5xx 不把原文漏出去。
//
// 【为什么 4xx 那两条也要写】它们是"透传"的反面例子——判据要是写成
// "只要认得出来就透传"，ErrUpstream（502）会跟着一起漏，
// 而它的原文里可能有上游的 URL 和 API Key 的报错信息。
func TestSafeDetail(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"4xx 透传最内层那句", http.StatusNotFound, fmt.Errorf("load knowledge base: %w", ErrNotFound), "load knowledge base: not found"},
		{"4xx 透传（裸 sentinel）", http.StatusBadRequest, ErrInvalid, "invalid argument"},
		{"502 不透传原文", http.StatusBadGateway, fmt.Errorf("call upstream: %w", ErrUpstream), InternalErrorDetail},
		{"500 不认识的错误不透传原文", http.StatusInternalServerError, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"), InternalErrorDetail},
		// 这一档是"status 由调用方给"的理由：platform 认不出 ctxmgr 的
		// sentinel，现算会得到 500、把原因抹掉。
		{"本包认不出的 4xx sentinel 也照透传", http.StatusBadRequest, errors.New("context budget exceeded"), "context budget exceeded"},
		{"nil", http.StatusInternalServerError, nil, InternalErrorDetail},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SafeDetail(tc.status, tc.err))
		})
	}
}
