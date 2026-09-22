// retention.go 是 conversation_events 的回收路径（issue #98）。
//
// 【为什么单开一个文件】摘要/偏好/记忆向量那三条周期任务服务的是"从历史
// 里提炼出更紧凑的表示"，剪枝服务的是"把已经过期的原始记录删掉"——目标
// 相反（一个留、一个删），放在 memory.go 里会让"周期任务都在内存这个文件"
// 这个印象掩盖掉两者的区别。
package conversation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// eventsRetention 是 conversation_events 的保留窗口。
//
// 【为什么必须 ≥ 幂等键的保留窗口】补发（replayRecordedTurn）读的就是
// 这张表：命中幂等键时装的是"那一轮记下来的事件"。如果那一轮的事件已经
// 被剪掉，补发循环会一条都读不到——然后它会一直等到 replayTimeout 才报
// 错，用户看到的是"重试了 10 分钟然后失败"，比直接说"这一轮太久远了"更糟。
// 所以窗口的下界不是"这轮跑完了没有"，而是 IdempotencyKeyTTL：键一过期，
// 那一轮的事件才不会再有第二个读者。
//
// 【两个窗口怎么就对上了】事件总是写在它那一轮的幂等键【之后】——键先
// 预留、再发第一条事件。所以"事件比它的键年轻"，按同一个窗口删事件，
// 永远不会删掉一个还有活键指向的事件。
//
// 【为什么不做"run 到终态就剪"】那条规则的代价正是上面这段：本轮一跑完
// 就删，24 小时内的同键重试全部退化成一个空的补发。用户视角里"重试"
// 和"过了一天再重试"没有区别，撞上哪个是运气。
const eventsRetention = platform.IdempotencyKeyTTL

// eventsPruneInterval 是剪枝任务的运行间隔。
//
// 【它决定的是"最多多留多久"，不是窗口本身】窗口 24 小时是硬语义；
// 这个间隔只决定表里最多积压到 24 小时 + 一个 tick 的事件（6 小时 =
// 最多 30 小时），也就是一天 4 次删除。间隔再短只是把同一条 DELETE 发得
// 更勤——这张表没有 created_at 索引，每次删除都是一次顺序扫描（见
// PgRepo.PruneConversationEvents 的说明），没有理由更勤。
const eventsPruneInterval = 6 * time.Hour

// pruneExpiredEvents 删掉保留窗口之外的事件行，由 StartMemoryMaintenance
// 注册的周期任务调用，也可以在测试里直接调用。
//
// 【删除是幂等的，失败也没关系】下一次 tick 会算出更早的截止时刻再删一次；
// 删掉的行本来就该消失，重复执行不会产生"删多了"的后果。所以这里不需要
// 游标、不需要断点续传，也不需要因为一次失败就升级成别的动作。
func (u *Usecase) pruneExpiredEvents(ctx context.Context) error {
	// 截止时刻在 Go 侧算好再传进 SQL——本包所有时间戳都由 Go 侧生成
	// （见 Repo.PruneConversationEvents 的注释），测试才能控制时间。
	cutoff := time.Now().Add(-eventsRetention)
	deleted, err := u.repo.PruneConversationEvents(ctx, u.db, cutoff)
	if err != nil {
		return fmt.Errorf("prune conversation events before %s: %w", cutoff, err)
	}
	if deleted > 0 {
		// 删了多少要有据可查：这是本包唯一一条会真的丢数据的操作，静默的
		// 批量删除以后没人能回答"我昨天的对话事件怎么没了"。
		slog.Default().Info("pruned expired conversation events",
			"deleted", deleted, "cutoff", cutoff)
	}
	return nil
}
