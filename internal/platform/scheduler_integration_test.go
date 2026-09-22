// 连真实 Postgres + River 表的集成测试。
//
// 测试库从哪来由 internal/testdb 决定（issue #70）：设了
// CONGORAG_TEST_DB_URL 就用它，没设就自己起一个容器。
//
// 这条测的是 scheduler_test.go 那几条单元测试测不到的部分：
// RegisterPeriodic 真的调用 river.Client.PeriodicJobs().Add() 时,
// 参数（尤其是 PeriodicJobOpts.ID）真的能被 River 接受、不报错——
// 单元测试里 sched.tasks 是手动摆放的,从没真正调用过这一行。
package platform

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/testdb"
)

func TestIntegration_RiverScheduler_RegisterPeriodic_DoesNotError(t *testing.T) {
	// 测试库从哪来由 internal/testdb 决定（issue #70）：设了
	// CONGORAG_TEST_DB_URL 就用它，没设就自己起一个容器；两者都保证
	// 返回时 schema（含 River 那套队列表）已经就绪。
	pool := testdb.Require(t)

	sched := NewRiverScheduler()
	workers := river.NewWorkers()
	river.AddWorker(workers, NewPeriodicTaskWorker(sched))

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	})
	require.NoError(t, err)
	sched.SetClient(client)

	// 用一个独一无二的名字，避免和别的测试跑之间留下的旧 PeriodicJobOpts.ID
	// 冲突（River 不允许重复 ID）。
	name := "integration-test-task-" + time.Now().Format("20060102150405.000000000")

	require.NotPanics(t, func() {
		sched.RegisterPeriodic(name, time.Hour, func(ctx context.Context) error { return nil })
	})
}
