package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ platform.PeriodicScheduler = (*fakeScheduler)(nil)

// fakeScheduler 不真的定时跑，只记住注册的函数——测试可以直接调用它来
// 模拟"这一轮周期任务被触发了"，不需要真的等 6 小时。
type fakeScheduler struct {
	registered map[string]func(ctx context.Context) error
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{registered: map[string]func(ctx context.Context) error{}}
}

func (f *fakeScheduler) RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error) {
	f.registered[name] = fn
}

// 剪枝任务必须真的被注册到调度器上——没有这一条，"事件表有了回收路径"
// 就只是代码里写着一个没人调用的函数（issue #98）。
func TestStartMemoryMaintenance_RegistersEventPrune(t *testing.T) {
	d := newTestUsecase()
	sched := newFakeScheduler()

	d.uc.StartMemoryMaintenance(context.Background(), sched)

	_, ok := sched.registered["conversation-events-prune"]
	assert.True(t, ok, "事件表剪枝必须有周期任务挂着，否则这条表又变回只增不减")
}

// 保留窗口必须 ≥ 幂等键的窗口：补发读的就是这张表，先删事件再让键失效
// 的话，24 小时内的同键重试会读不到任何事件、空转到 replayTimeout 才报错
// （issue #98 的目标里那句"run 到终态即可剪枝"的边界就在这里）。
func TestEventsRetention_CoversIdempotencyReplayWindow(t *testing.T) {
	assert.GreaterOrEqual(t, eventsRetention, platform.IdempotencyKeyTTL,
		"保留窗口短于幂等键窗口，重试就会撞上已经被删掉的事件")
}

// 剪枝删的是"窗口之外"的行：窗口内的必须一行不少。
func TestPruneExpiredEvents_DeletesOnlyEventsOlderThanRetention(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	require.NoError(t, d.uc.Send(context.Background(), conv.ID, "你好", "", newFakeSink()))

	d.repo.mu.Lock()
	before := len(d.repo.events[conv.ID])
	d.repo.mu.Unlock()
	require.NotZero(t, before, "前置条件：这一轮应该写下了事件")

	// 全部事件都还在窗口内：剪枝必须一行都不删。
	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()))
	d.repo.mu.Lock()
	assert.Len(t, d.repo.events[conv.ID], before, "窗口内的事件不能被删")
	d.repo.mu.Unlock()

	// 把它们整体推到一个窗口之前：这一轮事件必须被删干净。
	d.repo.backdateEvents(conv.ID, eventsRetention+time.Minute)
	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()))
	d.repo.mu.Lock()
	defer d.repo.mu.Unlock()
	assert.Empty(t, d.repo.events[conv.ID], "窗口之外的事件必须被回收")
}

// 截止时刻必须是「现在 - 保留窗口」，由 Go 侧算好再传进 SQL——写成别的值
// （比如 now()）会把整张表删空，而且没有任何东西会报错。
func TestPruneExpiredEvents_CutoffIsNowMinusRetention(t *testing.T) {
	d := newTestUsecase()
	start := time.Now()

	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()))

	d.repo.mu.Lock()
	cutoff := d.repo.pruneBefore
	d.repo.mu.Unlock()
	wantFrom := start.Add(-eventsRetention)
	wantTo := time.Now().Add(-eventsRetention)
	assert.False(t, cutoff.Before(wantFrom), "截止时刻不能早于 now - eventsRetention")
	assert.False(t, cutoff.After(wantTo), "截止时刻不能晚于 now - eventsRetention")
}

// 删了东西要记日志——这是本包唯一一条真的会丢数据的操作，静默的批量删除
// 之后没人能回答"我昨天的事件怎么没了"。这里只钉住"删了/没删"这条分支
// 不会 panic（真正去看日志是人的事），以及幂等：连删两次不报错。
func TestPruneExpiredEvents_IsIdempotent(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	require.NoError(t, d.uc.Send(context.Background(), conv.ID, "你好", "", newFakeSink()))
	d.repo.backdateEvents(conv.ID, eventsRetention+time.Minute)

	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()))
	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()), "重复剪枝不该报错")

	d.repo.mu.Lock()
	defer d.repo.mu.Unlock()
	assert.Empty(t, d.repo.events[conv.ID])
}

// 剪枝失败必须上报（由 River 重试），不能吞掉——静默失败会让这张表在
// 用户机器上继续无限增长，而所有判据都显示"任务在跑"。
func TestPruneExpiredEvents_ReportsFailure(t *testing.T) {
	d := newTestUsecase()
	d.repo.failOn = "PruneConversationEvents"
	d.repo.err = assert.AnError

	require.Error(t, d.uc.pruneExpiredEvents(context.Background()))
}

// 一个会话的事件不能被别的会话的剪枝带走（真表的 WHERE 只有 created_at，
// 但假实现按会话分组；这条钉住的是"剪枝不会顺手删掉窗口内的行"）。
func TestPruneExpiredEvents_KeepsEventsOfOtherConversations(t *testing.T) {
	d := newTestUsecase()
	oldConv, err := d.uc.CreateConversation(context.Background(), "旧会话", nil)
	require.NoError(t, err)
	newConv, err := d.uc.CreateConversation(context.Background(), "新会话", nil)
	require.NoError(t, err)

	require.NoError(t, d.uc.Send(context.Background(), oldConv.ID, "你好", "", newFakeSink()))
	require.NoError(t, d.uc.Send(context.Background(), newConv.ID, "你好", "", newFakeSink()))

	// 只把旧会话的事件推到窗口之外。
	d.repo.backdateEvents(oldConv.ID, eventsRetention+time.Minute)
	require.NoError(t, d.uc.pruneExpiredEvents(context.Background()))

	d.repo.mu.Lock()
	defer d.repo.mu.Unlock()
	assert.Empty(t, d.repo.events[oldConv.ID], "窗口外的旧会话事件该被删")
	assert.NotEmpty(t, d.repo.events[newConv.ID], "窗口内的新会话事件一行都不能少")
}
