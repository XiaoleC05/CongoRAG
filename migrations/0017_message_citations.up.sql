-- 0017_message_citations
--
-- 参赛版：把"这一轮回答的引用与引用校验结果"落到消息行上。
--
-- 【为什么需要持久化】citation/grounding 原本只存在于 SSE 流里：刷新页面后
-- 历史消息只剩正文里的 [n] 文字，点不了、也看不到校验徽标。对"可溯源"这个
-- 卖点来说，这是体验断点——评审回看时最想看的就是历史引用。
--
-- 【为什么是 jsonb 而不是拆表】引用是"这一轮回答的附属数据"，生命周期与
-- 消息完全一致、不会被单独查询或关联；拆表只多一次 JOIN。jsonb 的代价是
-- 不能对引用做 SQL 级检索，而本项目没有这个需求（检索走 document_chunks）。
ALTER TABLE messages
    ADD COLUMN citations jsonb,
    ADD COLUMN grounding jsonb;
