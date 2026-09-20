package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这个文件测两部分：不需要真实 River.Client 就能测的（lookup 分发、
// 装配顺序错误时的 panic），以及需要真实 Postgres + River 表才能测的
// 完整往返（scheduler_integration_test.go，用 CONGORAG_TEST_DB_URL 门控，
// 和 internal/llm 的集成测试同一个模式）。

// TestPeriodicTaskWorker_DispatchesToRegisteredFunction 直接操纵
// riverScheduler 的内部 map（同包测试文件，能访问未导出字段），
// 不需要真的注册进 river.Client——这条测的是"名字对上函数"这一步
// 分发逻辑本身，不是 River 的调度机制。
func TestPeriodicTaskWorker_DispatchesToRegisteredFunction(t *testing.T) {
	sched := NewRiverScheduler()
	called := false
	sched.tasks["my-task"] = func(ctx context.Context) error {
		called = true
		return nil
	}

	worker := NewPeriodicTaskWorker(sched)
	job := &river.Job[periodicTaskArgs]{Args: periodicTaskArgs{Name: "my-task"}}

	err := worker.Work(context.Background(), job)

	require.NoError(t, err)
	assert.True(t, called, "worker 必须真的调用了注册在这个名字下的函数")
}

// 分发函数自己的错误必须原样传出去，不能被 worker 吞掉——
// River 靠这个错误判断要不要重试这次任务。
func TestPeriodicTaskWorker_PropagatesTaskError(t *testing.T) {
	sched := NewRiverScheduler()
	wantErr := errors.New("task failed")
	sched.tasks["my-task"] = func(ctx context.Context) error { return wantErr }

	worker := NewPeriodicTaskWorker(sched)
	job := &river.Job[periodicTaskArgs]{Args: periodicTaskArgs{Name: "my-task"}}

	err := worker.Work(context.Background(), job)

	assert.ErrorIs(t, err, wantErr)
}

// 【这条测的是一个真实踩过的坑】River 表里出现一个没人认领的 kind/name
// 组合——之前 file_cleanup 那个 kind 就用真实的方式踩过一次这个坑
// （Schedule 插了任务，但漏了注册对应的 Worker，任务在 river_job 表里
// 卡成 retryable，日志刷 "Unhandled job kind" 却不影响进程存活）。
// 这条测的是同一形状在 periodic task 分发这一层：名字对不上时必须报错，
// 不能假装成功——假装成功的话，孤儿对账这种周期任务会"看起来在跑"，
// 实际上从未真正执行过。
func TestPeriodicTaskWorker_UnknownName_ReturnsError(t *testing.T) {
	sched := NewRiverScheduler()
	worker := NewPeriodicTaskWorker(sched)
	job := &river.Job[periodicTaskArgs]{Args: periodicTaskArgs{Name: "never-registered"}}

	err := worker.Work(context.Background(), job)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "never-registered")
}

// WorkerDefaults.Timeout 返回 0,而 River 把 0 换成 JobTimeoutDefault
// （1 分钟）。走这个桥的周期任务是"遍历全部会话、每个会话一次 LLM 调用"
// 的整表扫描（conversation-summary 每 5 分钟、conversation-preferences
// 每 10 分钟）,1 分钟的上限必然把排在后面的会话整轮截断。所以这个 Worker
// 必须真的覆写 Timeout——返回 0 就等于没覆写,只能靠运维从"job 记成
// completed 但日志刷 context deadline exceeded"里反推。
func TestPeriodicTaskWorker_Timeout_OverridesRiverDefault(t *testing.T) {
	worker := NewPeriodicTaskWorker(NewRiverScheduler())
	job := &river.Job[periodicTaskArgs]{Args: periodicTaskArgs{Name: "my-task"}}

	assert.Greater(t, worker.Timeout(job), time.Minute,
		"不能落回 River 的 1 分钟默认上限")
}

// 装配顺序错了（SetClient 之前就调用 RegisterPeriodic）必须显眼地炸掉，
// 不能安静地把注册请求丢在地上——那样"孤儿文件永远没人清"这种问题
// 要等几周后磁盘占满才会被人发现。
func TestRegisterPeriodic_PanicsBeforeSetClient(t *testing.T) {
	sched := NewRiverScheduler()

	assert.Panics(t, func() {
		sched.RegisterPeriodic("orphan-files", 0, func(ctx context.Context) error { return nil })
	})
}
