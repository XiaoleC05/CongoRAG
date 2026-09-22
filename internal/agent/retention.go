// retention.go 是 run_events 与 tool_effect_log 的回收路径（issue #98）。
//
// 【为什么单开一个文件】usecase.go 里那些写记录的代码服务的是"把一次运行
// 发生过的事留下来"；这里服务的是"把已经没人会读的记录删掉"——目标相反。
// 混在一起会让"这个包在写轨迹"这个印象掩盖掉两者方向的不同。形状照
// internal/conversation/retention.go 来，读一边能认出另一边。
//
// ══════════════════════════════════════════════════════════════════
// 【两张表的判据为什么不一样——这是本文件最要紧的一段】
//
// 会话那一侧只有一张事件表，判据是一维的（created_at < cutoff）。agent
// 这一侧有两张表，而它们的读者完全不同：
//
//   run_events        的读者是**补发**（post /agents/{id}/runs 命中幂等键时
//                     把那一轮记录的事件重放一遍）与 run 级断线重订阅。
//                     它们与"这条 run 是什么状态"无关，只与"客户端还会不会
//                     来读"有关。所以判据是时间：键一过期，那张表就不会再有
//                     第二个读者 → runEventsRetention = IdempotencyKeyTTL。
//
//   tool_effect_log   的读者只有一个：恢复路径的 gateToolReplay。而它只在
//                     run 是 interrupted 时才会被调用（PrepareResume 的第一
//                     道判断）。终态是**吸收态**（model.go 的 runTransitions
//                     里 completed/failed/cancelled 没有任何出边），所以一条
//                     终态 run 的账本再也不会被读到；反过来，一条 interrupted
//                     的 run 无论多旧都可能被用户点"恢复"。
//
// **按时间剪账本会把恢复机制反过来打脸。** 假设窗口设成 24 小时：一条
// interrupted 的 run 在第 3 天被恢复，gateToolReplay 去查账本，返回 false
// ——它读到的是"这一步没执行过"，而事实是执行过、只是被我们删了。于是
// 工具真的被第二次执行，全程无错误、无日志。这正是 0010 迁移里那句
// "一个会静默重复执行的 resume，比没有 resume 更糟"要防的事。
//
// 所以账本的判据是 **run 已终态**，第二道闸（toolEffectLogRetention）只是
// 在终态之上多留一段时间供排查，不是安全下界。
// ══════════════════════════════════════════════════════════════════
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// runEventsRetention 是 run_events 的保留窗口。
//
// 【为什么必须 ≥ 幂等键的保留窗口】补发读的就是这张表（见文件头）。补发
// 循环一条都读不到时不会报错，只会发出一帧 run_started 就结束——客户端
// 拿不到"这一轮被删了"和"这一轮本来就没什么内容"之间的任何区别。
// 所以窗口的下界不是"这轮跑完了没有"，而是 IdempotencyKeyTTL：键一过期，
// 那一轮的事件才不会再有第二个读者。
//
// 【与会话那一侧共用同一个平台常量】ADR-008 决策二要求两条幂等路径的窗口
// 是同一个值，不是一个各写一遍的 24 * time.Hour——两边都叫幂等，漂移不会
// 被任何编译错误或测试发现。
//
// 【两个窗口怎么就对上了】事件总是写在它那一轮的幂等键**之后**——键先
// 预留（claimRun 的 ReserveIdempotencyKey）、再发第一条事件（emitRunEvent）。
// 所以"事件比它的键年轻"，按同一个窗口删事件，永远不会删到一个还有活键
// 指向的事件。反过来说，如果允许事件比键先存在，这个一刀切的时间判据就
// 不成立了，得改成跟着每个键走。
const runEventsRetention = platform.IdempotencyKeyTTL

// toolEffectLogRetention 是终态 run 的效果账本在回收之前额外多留的时间。
//
// 【它是余量，不是安全下界】安全下界是"run 已终态"（见文件头）。终态之后
// 账本已经没有任何读者，理论上可以立刻删；这里再等 24 小时是为了让刚结束
// 的运行留一份完整现场——排查"某一步到底跑没跑"时，账本是除 step 状态之外
// 的唯一证据。
//
// 【为什么和 checkpointRetention 取同一个值】两者在同一次收尾里一起被写、
// 服务的是同一类排查动作（"这次运行最后发生了什么"）。取同一个数字省得
// 将来回答"为什么账本留了 24 小时而快照只留 6 小时"。
const toolEffectLogRetention = 24 * time.Hour

// retentionPruneInterval 是剪枝任务的运行间隔。
//
// 【它决定的是"最多多留多久"，不是窗口本身】两个窗口都是硬语义；这个间隔
// 只决定表里最多积压到 窗口 + 一个 tick（24 小时 + 6 小时 = 30 小时），
// 也就是一天四次删除。它们都是"判据算出来结果几乎不变"的整表操作，
// 跑得更勤只是把同一条 DELETE 发得更勤——所以取小时级，和会话那一侧
// 的 eventsPruneInterval 同一个数量级。
const retentionPruneInterval = 6 * time.Hour

// pruneExpiredRunEvents 删掉保留窗口之外的 run 事件行。
//
// 【删除是幂等的，失败也没关系】下一次 tick 会算出更早的截止时刻再删一次；
// 删掉的行本来就该消失，重复执行不会产生"删多了"的后果。所以不需要游标、
// 不需要断点续传。
func (u *Usecase) pruneExpiredRunEvents(ctx context.Context) error {
	// 截止时刻在 Go 侧算好再传进 SQL——本包所有时间戳都由 Go 侧生成
	// （与 conversation 的 Repo.PruneConversationEvents 同一套理由），
	// 测试才能控制时间。
	cutoff := time.Now().Add(-runEventsRetention)
	deleted, err := u.repo.PruneRunEvents(ctx, u.db, cutoff)
	if err != nil {
		return fmt.Errorf("prune run events before %s: %w", cutoff, err)
	}
	if deleted > 0 {
		// 删了多少要有据可查：这是本包少数几条会真的丢数据的操作之一，
		// 静默的批量删除以后没人能回答"我昨天那次运行的事件怎么没了"。
		u.log(ctx).Info("pruned expired run events", "deleted", deleted, "cutoff", cutoff)
	}
	return nil
}

// pruneToolEffectLog 回收终态 run 的工具效果账本。
//
// 【判据是终态，不是年龄】理由见文件头的那一整段：按年龄剪 interrupted
// run 的账本 = 让恢复路径重放一个已经生效过的工具，而且不留任何痕迹。
func (u *Usecase) pruneToolEffectLog(ctx context.Context) error {
	olderThan := time.Now().Add(-toolEffectLogRetention)
	deleted, err := u.repo.PruneToolEffectLog(ctx, u.db, olderThan)
	if err != nil {
		return fmt.Errorf("prune tool effect log of runs terminal before %s: %w", olderThan, err)
	}
	if deleted > 0 {
		// 同样要留痕：账本被删掉之后，"这一步执行过没有"就只剩 step 状态
		// 一个证据了，事后能对上"什么时候删的"很重要。
		u.log(ctx).Info("pruned tool effect log of terminal runs", "deleted", deleted, "older_than", olderThan)
	}
	return nil
}

// pruneRetention 是周期任务的执行体：两张表各剪一次。
//
// 【为什么合成一条任务，而不是各注册一条】两者的触发条件不同，但都是
// "把没人会读的行删掉"、都挂在同一个间隔上，而且都由本包拥有。合成一条的
// 好处是装配根只注册一次——漏注册一条的表现是静默的（那张表继续无限增长，
// 没有任何东西会报错），注册点越少越不容易漏。
//
// 【为什么用 errors.Join 而不是串行早退】两张表的判据互不相关（一张按时间、
// 一张按 run 状态），前一张失败不该让后一张这一轮也不做。任务本身仍然报
// 失败，由周期任务的正常失败路径记下并重试。
func (u *Usecase) pruneRetention(ctx context.Context) error {
	return errors.Join(
		u.pruneExpiredRunEvents(ctx),
		u.pruneToolEffectLog(ctx),
	)
}

// StartRetention 把保留窗口剪枝挂到周期调度器上（issue #98）。
//
// 【为什么注册点在包里而不是装配根】与会话那一侧
// （conversation.Usecase.StartMemoryMaintenance）同一个形状：本包拥有的、
// 后台按固定间隔跑的维护工作，注册点放在包内，装配根只调这一个方法——
// 加一张要剪的表时不需要回头改装配根。
//
// 【它和 agent-checkpoint-prune 并列，不是同一条】那条是"回收终态 run 的
// 快照"，判据、表、回收动作都不同（那一条只把 state_snapshot 置空，不删
// 行）。放在同一条任务里会让"剪枝到底动了什么"在日志上分不开。
func (u *Usecase) StartRetention(ctx context.Context, sched platform.PeriodicScheduler) {
	sched.RegisterPeriodic("agent-retention-prune", retentionPruneInterval, func(ctx context.Context) error {
		return u.pruneRetention(ctx)
	})
}
