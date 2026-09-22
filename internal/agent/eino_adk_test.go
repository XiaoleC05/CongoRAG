// eino_adk_test.go 钉住 eino_adk.go 这条边界上的两件事：工具失败怎么交回
// 模型（issue #15），以及 ctx 取消后生产者怎么退出（issue #35）。测试要
// 构造真实的 adk.AsyncIterator，所以它和被测文件一样 import Eino——usecase.go
// 依然不认识这些类型。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// stubTool 是按脚本失败的工具，只用来观察 toolAdapter 怎么处理错误。
type stubTool struct {
	err error
}

func (s *stubTool) Name() string        { return "stub" }
func (s *stubTool) Description() string { return "测试用工具" }
func (s *stubTool) Metadata() Metadata {
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetryNever}
}
func (s *stubTool) Spec() ToolSpec {
	return ToolSpec{Name: "stub", Description: "测试用工具", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (s *stubTool) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return nil, s.err
}

// 参数级失败（除零、非法 UUID）必须变成一次普通的 tool result：作为 Go
// error 交回 Eino 的话，ToolsNode 会当成致命错误、不写 tool message，整张
// ReAct 图直接失败，模型永远拿不到失败原因，也就没法改参数重试
// （issue #15）。
func TestToolAdapter_InvalidArgument_BecomesToolResult(t *testing.T) {
	a := &toolAdapter{t: &stubTool{err: fmt.Errorf("division by zero: %w", platform.ErrInvalid)}}

	got, err := a.InvokableRun(context.Background(), `{"a":10,"b":0,"operator":"/"}`)
	require.NoError(t, err, "工具错误不能交回 Eino，否则整轮 run 结束")

	var payload toolError
	require.NoError(t, json.Unmarshal([]byte(got), &payload), "结果要是模型能读的 JSON 对象: %s", got)
	assert.Contains(t, payload.Error, "division by zero")
}

// 基础设施故障仍然原样返回错误：检索库连不上、ctx 取消之类重试没有意义，
// 整轮该按上游故障结束，而不是让模型对着一个假的结果继续编。
func TestToolAdapter_InfrastructureError_StillReturnsError(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	a := &toolAdapter{t: &stubTool{err: boom}}

	_, err := a.InvokableRun(context.Background(), `{}`)
	require.ErrorIs(t, err, boom)
}

// ctx 已取消时不再降级成工具结果：这次运行正在被拆掉，不该让模型再跑一轮。
func TestToolAdapter_CancelledContext_StillReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &toolAdapter{t: &stubTool{err: fmt.Errorf("bad args: %w", platform.ErrInvalid)}}

	_, err := a.InvokableRun(ctx, `{}`)
	require.ErrorIs(t, err, platform.ErrInvalid)
}

// 客户端断开后 out 不再有接收者，生产者必须能自己退出：无条件 send 会让
// 这个 goroutine 永久阻塞在 channel send 上，连带 Eino 的 iterator 一起
// 泄漏，每中断一次泄漏一套（issue #35）。
func TestDrainIterator_ExitsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	// 故意不给缓冲：模拟"消费者（consumeEvents）已经返回，没人再读"。
	out := make(chan adkEvent)

	exited := make(chan struct{})
	go func() {
		drainIterator(ctx, iter, out)
		close(exited)
	}()

	cancel()
	gen.Send(&adk.AgentEvent{Err: errors.New("boom")})

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 drainIterator 仍停在发送上：goroutine 与 Eino iterator 泄漏")
	}
}

// ════════════════════════════════════════════════════════════════
// issue #109：工具结果的大小上限
// ════════════════════════════════════════════════════════════════

// hugeResultTool 返回一份远超上限的结果——conversation_search 在 500 条
// 消息里返回完整正文时就是这个量级。
type hugeResultTool struct{ stubTool }

func (s *hugeResultTool) Name() string { return "huge" }
func (s *hugeResultTool) Metadata() Metadata {
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetrySafe}
}

func (s *hugeResultTool) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	items := make([]conversationSearchResultItem, 0, 500)
	for i := 0; i < 500; i++ {
		items = append(items, conversationSearchResultItem{
			Role: "assistant", Content: strings.Repeat("很长的一段正文内容", 40), SequenceNo: int64(i),
		})
	}
	return json.Marshal(items)
}

// 【issue #109 的验收判据】超大的工具结果必须被截断，而且**显式告诉模型**
// 结果被截断了——静默丢内容会让模型以为自己已经看到了全部。
//
// 【为什么不截断的后果比"太大"更麻烦】上游的错误会经 agentEventError 落到
// upstream_llm_error，前端按 type 提示用户"检查 API Key 和配额"，而真正的
// 原因只是这份结果太大（issue #34 修掉的是同一类误报）。
func TestToolAdapter_HugeToolResult_IsCappedWithExplicitNotice(t *testing.T) {
	raw, err := (&hugeResultTool{}).Invoke(context.Background(), nil)
	require.NoError(t, err)
	require.Greater(t, len(raw), maxToolResultBytes, "这条测试的前提是结果真的超限")

	a := &toolAdapter{t: &hugeResultTool{}}
	got, err := a.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)

	assert.LessOrEqual(t, len(got), maxToolResultBytes,
		"截断后的大小是硬上限——不是「接近」上限")
	assert.Less(t, len(got), len(raw), "必须真的截了，不能原样交给模型")

	var capped truncatedToolResult
	require.NoError(t, json.Unmarshal([]byte(got), &capped),
		"截断后的结果必须是合法 JSON：tool_result 那一列是 jsonb")
	assert.True(t, capped.Truncated)
	assert.Contains(t, capped.Notice, "截断", "要显式告诉模型结果被截断了，不能静默丢弃")
	assert.NotEmpty(t, capped.Preview, "要留下前面那一段，模型才知道截掉的是什么")

	assert.False(t, strings.ContainsRune(capped.Preview, '�'),
		"刀口要落在 rune 边界上，否则模型会读到一串替换字符")
}

// 没超限的结果原样通过——截断只处理超限的那一份，不动正常路径。
func TestCapToolResult_UnderLimit_IsUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"result":3}`)
	assert.Equal(t, []byte(raw), capToolResult(raw))
}
