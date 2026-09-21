package llm

import "errors"

// ErrEmbeddingResetRequired 表示"换 embedding 模型需要先明确同意清空重建"
// （issue #39）。
//
// 【为什么不用 platform.ErrConflict】它要走到一个独立的 Problem type
// （embedding_change_requires_reindex），因为前端要按它弹一个确认框、
// 让用户明确同意"清空已有向量并自动重建"，然后带 allowEmbeddingReset
// 重发一次；用一个笼统的 conflict 前端分不出来。
//
// 这和 ctxmgr.ErrOverflow 的处理方式是同一个模式：包自己的 sentinel
// 在 apps/api 的 classify() 里特判一档（见 problem.go）。
//
// 【为什么不是静默放行】USING NULL 抹掉向量就是永久丢失，而"换模型"这个
// 动作本身是合理的——所以既不能拒绝到底，也不能替用户决定，只能把代价
// 说清楚让他确认。
var ErrEmbeddingResetRequired = errors.New("embedding reset required")
