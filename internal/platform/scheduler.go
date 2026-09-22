// scheduler.go 把"注册一个按固定间隔执行的函数"这个通用需求接到 River
// 的周期任务机制上。消费方是各业务包的周期任务注册入口
// （knowledge.Usecase.StartReconciler、conversation.Usecase.StartMemoryMaintenance、
// worker 装配根里的 agent-checkpoint-prune），但接口本身不认识这些任务
// 是什么——它只认识"名字 + 间隔 + 一个函数"，这样将来别的模块要加周期任务
// 时不需要碰这个文件。
package platform

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// PeriodicScheduler 让 usecase 层注册周期任务,不需要认识 River。
//
// 【为什么不直接把 usecase 的 fn 注册成 river.Worker】River 的 Worker
// 必须在构造 river.Client 之前就用 river.AddWorker 静态注册好（工作原理是
// 泛型 + 编译期类型绑定,不支持运行期动态加一个新的 Worker 类型)。
// 但 usecase 层的 RegisterPeriodic 调用发生在 river.Client 造好【之后】——
// 装配顺序上,riverScheduler 必须先用【一个】固定的 Worker 类型
// （periodicTaskWorker）占好位置,RegisterPeriodic 只是往一个运行期的
// map 里加"名字 → 函数"的映射,由那个固定 Worker 在任务真正触发时查表分发。
type PeriodicScheduler interface {
	RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error)
}

// MaintenanceQueue 是周期维护任务的专用队列名（issue #129）。
//
// 【为什么队列名的定义处在这里，而不是 worker 的装配根】任务进哪条队列是
// **插入时**决定的，而周期任务的插入点就在本文件（RegisterPeriodic 给出的
// InsertOpts）。装配顺序反过来引用是不通的：apps/worker/internal/app 属于
// apps/worker 的 internal 子树，Go 只允许 apps/worker 自己 import 它，本包
// 引用它会直接编译不过（internal 可见性 + 包循环两重障碍）。所以名字定义在
// 插入点这一侧，worker 的 Config.Queues 反过来引用它——两边因此共用一个
// 定义，而不是两个必须碰巧一致的字面量。
//
// 【对不上会怎样】任务被投进一条没有任何消费者注册的队列：不报错、不执行、
// 日志里也只有一条普通的成功插入。对账、摘要这些维护工作会静默停摆，
// 而界面上看不出任何异样——这正是它必须由编译器而不是靠人眼保证的原因。
const MaintenanceQueue = "maintenance"

// periodicTaskArgs 是桥接用的统一 job 类型。它的 Name 字段是任务名,
// 真正要执行的逻辑通过 riverScheduler 的 tasks map 在运行期查到。
type periodicTaskArgs struct {
	Name string `json:"name"`
}

func (periodicTaskArgs) Kind() string { return "platform_periodic_task" }

// riverScheduler 是 PeriodicScheduler 唯一的实现。
//
// 两阶段构造是故意的：NewRiverScheduler() 不需要 river.Client 就能造出来
// （因为它自己就是要被注册成 Worker 的那个对象,必须在 Client 构造之前存在）；
// SetClient 在 Client 造好之后调用一次,把它接上——这就是装配根里
// "先造 scheduler+worker，再造 client，再 SetClient" 这个顺序的由来。
type riverScheduler struct {
	mu     sync.Mutex
	tasks  map[string]func(ctx context.Context) error
	client *river.Client[pgx.Tx]
}

func NewRiverScheduler() *riverScheduler {
	return &riverScheduler{tasks: make(map[string]func(ctx context.Context) error)}
}

// SetClient 接上真正的 river.Client。必须在第一次 RegisterPeriodic 调用
// 之前完成——装配根里的顺序保证了这一点（见 apps/worker/internal/app/app.go）。
func (s *riverScheduler) SetClient(client *river.Client[pgx.Tx]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = client
}

func (s *riverScheduler) RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error) {
	s.mu.Lock()
	s.tasks[name] = fn
	client := s.client
	s.mu.Unlock()

	// client 为 nil 说明装配顺序错了——SetClient 应该总是先于任何
	// RegisterPeriodic 调用。这里 panic 而不是静默丢弃：静默丢弃会让
	// "孤儿文件永远没人清"这种问题只在几周后磁盘占满时才被发现。
	if client == nil {
		panic("platform: RegisterPeriodic called before SetClient — 装配顺序错了")
	}

	client.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(every),
		periodicTaskConstructor(name),
		&river.PeriodicJobOpts{ID: name},
	))
}

// periodicTaskConstructor 造出"这次到点了要插什么任务"的描述函数。
//
// 【为什么插入时就要指定队列】队列不是任务的属性，是**插入那一刻**写进
// river_job 行的（river 的 insertParamsFromConfigArgsAndOptions：
// insertOpts.Queue 优先于 args 自带的、再退到 default）。返回 nil
// InsertOpts 等于把任务放回 default——文档处理也在那条队列上，而单份文档
// 的上限是 30 分钟（internal/knowledge/river.go 的 documentProcessingTimeout），
// 一次批量上传就能把 default 的 10 个名额占满数小时。这正是 issue #129 要
// 治的问题：维护任务没坏，只是排在那后面，界面上完全看不出来。
//
// 【为什么是"经由这个桥的任务全都进 maintenance"，而不是按任务名挑】本桥
// 服务的全部是"按固定间隔、在后台遍历一小批数据做整理"的活（摘要压缩、
// 偏好抽取、记忆向量补算、事件/checkpoint 回收、孤儿文件对账）——它们都是
// 短周期任务。而真正会把队列占满的长任务只有文档处理，它不走这个桥
// （knowledge.RiverEnqueuer 直接入队），所以"default 留给文档处理、
// maintenance 留给周期任务"这条分工在这里天然成立。
//
// 【为什么不反过来列一张名单】名单漏一个的后果是静默的：那个任务悄悄回到
// default，被文档处理压在后面几小时。默认落在维护队列、需要时再显式改，
// 出错的代价小得多。
//
// 【为什么单独抽一个函数】RegisterPeriodic 必须先有 river.Client 才能调用
// （没有 client 会 panic），于是"插入时带不带队列"在单元测试里没有任何
// 观察点。抽出来之后测试可以直接断言这个返回值——队列路由退回 nil 是
// 一次单测能拦住的改动。
func periodicTaskConstructor(name string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return periodicTaskArgs{Name: name}, &river.InsertOpts{Queue: MaintenanceQueue}
	}
}

func (s *riverScheduler) lookup(name string) (func(ctx context.Context) error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn, ok := s.tasks[name]
	return fn, ok
}

// PeriodicTaskWorker 是唯一处理 periodicTaskArgs 的 River Worker。
// 必须在造 river.Client 之前用 river.AddWorker 注册它（拿它去配 sched）。
type PeriodicTaskWorker struct {
	river.WorkerDefaults[periodicTaskArgs]
	sched *riverScheduler
}

// NewPeriodicTaskWorker 把 worker 和它要分发的 scheduler 绑在一起。
//
// 【为什们worker 和 scheduler 是两个类型】river.AddWorker 是泛型函数，
// 按 JobArgs 类型分发——如果让 riverScheduler 自己实现 Work 方法，
// 它就必须绑定 periodicTaskArgs 这一个具体类型，将来万一想让 scheduler
// 服务另一种 job 类型会很别扭。分开之后 scheduler 只管"名字到函数的映射"，
// worker 只管"River 调用协议"，两者各自清楚。
func NewPeriodicTaskWorker(sched *riverScheduler) *PeriodicTaskWorker {
	return &PeriodicTaskWorker{sched: sched}
}

// periodicTaskTimeout 是走这个桥的周期任务的执行上限。
//
// 【为什么必须显式声明,不能继承默认值】PeriodicTaskWorker 内嵌
// WorkerDefaults,而 WorkerDefaults.Timeout 返回 0;River 看到 0 就换成
// JobTimeoutDefault（1 分钟）。可经由这个桥注册的任务恰恰都是"遍历全部
// 会话、每个会话再发起一次 LLM 调用"的整表扫描（conversation-summary
// 每 5 分钟、conversation-preferences 每 10 分钟），一轮超过 1 分钟是
// 常态;超时后 job ctx 被取消,排在后面的会话这一轮全部失败。给一个
// "一轮肯定干得完"的上限,而不是沿用那个为单条任务设计的默认值。
const periodicTaskTimeout = 30 * time.Minute

// Timeout 覆写 WorkerDefaults 的 0 值——0 会让 River 拿 1 分钟的默认上限
// 截断整表扫描（见 periodicTaskTimeout 的注释）。
func (w *PeriodicTaskWorker) Timeout(*river.Job[periodicTaskArgs]) time.Duration {
	return periodicTaskTimeout
}

func (w *PeriodicTaskWorker) Work(ctx context.Context, job *river.Job[periodicTaskArgs]) error {
	fn, ok := w.sched.lookup(job.Args.Name)
	if !ok {
		// 到这一步说明有一个周期任务的定义在 River 里,但对应的
		// RegisterPeriodic 调用没有发生——版本升级时如果改名了周期任务
		// 但没清理旧的 PeriodicJobOpts.ID,可能出现这种情况。
		return fmt.Errorf("platform: no periodic task registered for name %q", job.Args.Name)
	}
	return fn(ctx)
}
