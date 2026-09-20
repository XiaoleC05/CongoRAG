// 连真实 Postgres + River 表的集成测试。和 internal/llm 的同名文件一样的
// 门控方式：CONGORAG_TEST_DB_URL 未设置就跳过。
//
// 这条测的是 scheduler_test.go 那几条单元测试测不到的部分：
// RegisterPeriodic 真的调用 river.Client.PeriodicJobs().Add() 时,
// 参数（尤其是 PeriodicJobOpts.ID）真的能被 River 接受、不报错——
// 单元测试里 sched.tasks 是手动摆放的,从没真正调用过这一行。
package platform

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/stretchr/testify/require"
)

func TestIntegration_RiverScheduler_RegisterPeriodic_DoesNotError(t *testing.T) {
	dbURL := os.Getenv("CONGORAG_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("CONGORAG_TEST_DB_URL 未设置,跳过需要真实 Postgres + River 表的集成测试")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(ctx))

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
