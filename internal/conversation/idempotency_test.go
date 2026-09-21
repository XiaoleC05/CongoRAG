package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 幂等键重放（issue #37）
//
// 核心契约只有两条：
//   ① 同一个键的第二次请求不重新执行生成；
//   ② 它拿到的是那一轮已经记录下来的事件，而不是一条错误。
// 其余几条钉的是边界：作用域、指纹、保留窗口、没有键时的向后兼容。
//
// 【为什么不并进 usecase_test.go】那一份已经 1300 多行，而这一组
// 自成一块（复用同一套假实现，只多了一个幂等表的假存储）。
// ════════════════════════════════════════════════════════════════

func newConversationFor(t *testing.T, d *testDeps) uuid.UUID {
	t.Helper()
	c, err := d.uc.CreateConversation(context.Background(), "幂等测试会话", nil)
	require.NoError(t, err)
	return c.ID
}

func countEventsOfType(evs []Event, typ string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// 第二次同键发送必须「什么都不重做」：不再落消息，也不再发起一次生成。
//
// 【为什么 streamCalls 是这里唯一可信的观测量】消息条数没变也可能是因为
// 别的原因（中途报错之类），只有「生成没有被发起」直接对应用户真正关心的
// 那件事——不要重复扣一次 BYOK 的额度。
func TestSend_IdempotentHit_DoesNotReexecuteGeneration(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	first := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", first))

	callsAfterFirst := d.registry.chatModel.streamCalls
	msgsAfterFirst := len(d.repo.messages[convID])
	require.Equal(t, 1, callsAfterFirst, "第一次发送应该真的跑了一次生成")
	require.NotZero(t, msgsAfterFirst)

	second := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", second))

	assert.Equal(t, callsAfterFirst, d.registry.chatModel.streamCalls,
		"命中幂等键不能再发起一次生成")
	assert.Equal(t, msgsAfterFirst, len(d.repo.messages[convID]),
		"命中不能往会话里再写一轮消息")
}

// 补发的内容必须和第一次收到的逐条一致：同样的 event_id、同样的类型、
// 同样的 payload。任一不一致，客户端的续传游标就会和事件表分叉。
func TestSend_IdempotentHit_ReplaysRecordedEventsInOrder(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	first := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", first))
	require.NotEmpty(t, first.emitted)

	second := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", second))

	require.Len(t, second.emitted, len(first.emitted), "补发的帧数必须和原请求一致")
	for i := range first.emitted {
		assert.Equal(t, first.emitted[i].ID, second.emitted[i].ID, "第 %d 帧的 event_id", i)
		assert.Equal(t, first.emitted[i].Type, second.emitted[i].Type, "第 %d 帧的类型", i)
		assert.JSONEq(t, string(first.emitted[i].Payload), string(second.emitted[i].Payload),
			"第 %d 帧的 payload", i)
	}
}

// 命中是正常路径，不是错误路径。出现任何 error 帧（尤其是
// conflict_duplicate_key 或 internal_error）都说明 23505 的分流走错了。
func TestSend_IdempotentHit_DoesNotEmitErrorFrame(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", newFakeSink()))

	second := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", second))

	assert.Zero(t, countEventsOfType(second.emitted, "error"),
		"命中重复键不该推 error 帧——它是重放，不是冲突")
	assert.Positive(t, countEventsOfType(second.emitted, eventDoneName),
		"补发必须以终态事件收尾，否则客户端会停在无声的空流上")
}

// 这一轮还没结束时，补发要边等边发——这正是「第一次请求的响应丢了、
// 但生成还在继续」那个真实场景。
func TestSend_IdempotentHit_TailsUntilTerminalEvent(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	// 先正常跑完一轮，拿到它的事件序列。
	first := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", first))

	// 把这一轮的终态事件从库里摘掉，模拟「生成还在进行中」。
	d.repo.mu.Lock()
	stored := d.repo.events[convID]
	doneAt := -1
	for i, ev := range stored {
		if ev.Type == eventDoneName {
			doneAt = i
			break
		}
	}
	require.Positive(t, doneAt, "前置条件：第一轮应该产生了 done 事件")
	late := append([]Event(nil), stored[doneAt:]...)
	d.repo.events[convID] = append([]Event(nil), stored[:doneAt]...)
	d.repo.mu.Unlock()

	// 过一会儿再把终态事件补上——补发循环必须一直等到它。
	go func() {
		time.Sleep(3 * replayPollInterval)
		d.repo.mu.Lock()
		defer d.repo.mu.Unlock()
		d.repo.events[convID] = append(d.repo.events[convID], late...)
	}()

	second := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", second))

	assert.Equal(t, countEventsOfType(first.emitted, eventDoneName),
		countEventsOfType(second.emitted, eventDoneName),
		"补发应该等到后来才写进来的 done，而不是提前收场")
	assert.Len(t, second.emitted, len(first.emitted))
}

// 客户端在补发途中断开时，循环必须立刻退出——不检查 Done() 的话，
// 每个重复请求都会泄漏一个 goroutine 和一条连接，直到 replayTimeout。
func TestSend_IdempotentHit_ClientDisconnects_StopsReplay(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	first := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", first))

	// 把终态事件摘掉，否则补发会立刻撞到 done、测不到「等待」那一段。
	d.repo.mu.Lock()
	kept := make([]Event, 0, len(d.repo.events[convID]))
	for _, ev := range d.repo.events[convID] {
		if ev.Type != eventDoneName {
			kept = append(kept, ev)
		}
	}
	d.repo.events[convID] = kept
	d.repo.mu.Unlock()

	second := newFakeSink()
	close(second.done) // 客户端已经走了

	start := time.Now()
	err := d.uc.Send(context.Background(), convID, "你好", "key-1", second)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 2*time.Second,
		"客户端已断开，补发循环必须立刻退出，而不是一直等到 replayTimeout")
}

// 同一个键配不同的正文必须报错。静默重放旧答案是这里最坏的失败模式：
// 用户新敲的那句话既没落库、也不报错，界面上只是旧回答又出现了一遍。
func TestSend_IdempotentHit_FingerprintMismatch_IsInvalidArgument(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", newFakeSink()))

	second := newFakeSink()
	err := d.uc.Send(context.Background(), convID, "换了一句话", "key-1", second)

	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrInvalid, "同键不同正文是参数问题，不是重放")
	assert.Zero(t, countEventsOfType(second.emitted, "token"),
		"不该把上一轮的正文重放给他")
	assert.Equal(t, 1, countEventsOfType(second.emitted, "error"),
		"必须明确告诉客户端这次没执行")
}

// 作用域是「同一个会话 + 同一个键」：endpoint 里编了会话 id，
// 所以另一个会话用同一个键不能命中。
func TestSend_SameKeyDifferentConversation_IsNotAHit(t *testing.T) {
	d := newTestUsecase()
	convA := newConversationFor(t, d)
	convB := newConversationFor(t, d)

	require.NoError(t, d.uc.Send(context.Background(), convA, "你好", "同一个键", newFakeSink()))
	callsAfterA := d.registry.chatModel.streamCalls

	require.NoError(t, d.uc.Send(context.Background(), convB, "你好", "同一个键", newFakeSink()))

	assert.Equal(t, callsAfterA+1, d.registry.chatModel.streamCalls,
		"另一个会话用同一个键应该正常执行，而不是被当成重放")
}

// 空正文在校验阶段就被拒了，那时还没进锁、也就没预留键——
// 改好正文之后同一个键必须还能用。
func TestSend_EmptyText_DoesNotConsumeTheKey(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	err := d.uc.Send(context.Background(), convID, "   ", "key-1", newFakeSink())
	require.ErrorIs(t, err, platform.ErrInvalid)

	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", newFakeSink()),
		"失败的请求不该占掉这个键")
	assert.Equal(t, 1, d.registry.chatModel.streamCalls)
}

// 不带这个头的调用方行为必须和加这个功能之前完全一样——
// 这条是向后兼容的回归线。
func TestSend_WithoutKey_ReservesNothing(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "", newFakeSink()))

	d.repo.mu.Lock()
	defer d.repo.mu.Unlock()
	assert.Empty(t, d.repo.idempotency, "没带键就不该写幂等行")
}

// 预留时记下的游标必须是「本轮开始之前、该会话已经发到几号」。
// 它既不是 0、也不是本轮之后的号——补发全靠这个数定位起点。
func TestSend_ReservationRecordsEventCursor(t *testing.T) {
	d := newTestUsecase()
	convID := newConversationFor(t, d)

	// 手工制造两条「上一轮」的事件。
	const previousEvents = 2
	for i := 0; i < previousEvents; i++ {
		id, err := d.repo.NextEventID(context.Background(), nil, convID)
		require.NoError(t, err)
		require.NoError(t, d.repo.AppendEvent(context.Background(), nil, convID,
			Event{ID: id, Type: "token", Payload: []byte(`{"type":"token","data":{"text":"旧"}}`)}))
	}

	require.NoError(t, d.uc.Send(context.Background(), convID, "你好", "key-1", newFakeSink()))

	d.repo.mu.Lock()
	rec, ok := d.repo.idempotency[idempotencyKeyOf(messagesEndpoint(convID), "key-1")]
	d.repo.mu.Unlock()

	require.True(t, ok, "带键的请求应该在幂等表里留下一条记录")
	assert.Equal(t, int64(previousEvents), rec.FirstEventID,
		"游标必须是本轮开始前已发出的最后一个 event_id，补发才有正确的起点")
	assert.Equal(t, resourceTypeAssistantMessage, rec.ResourceType)
	assert.Equal(t, fingerprint("你好"), rec.RequestFingerprint)
	assert.Equal(t, messagesEndpoint(convID), rec.Endpoint, "endpoint 里必须带会话 id")
}
