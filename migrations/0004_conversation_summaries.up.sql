-- 0004_conversation_summaries
--
-- M3 长对话压缩：conversation_summaries 是"刚才聊了什么"的持久化存档，
-- 和 memories 表分工不同——见开发文档 §6.4："Summary 管刚才聊了什么，
-- Memory 管这个用户长期的偏好"。memories 表在 0001 就建好了，这里
-- 只补 Summary 那一半。
--
-- 一个会话最多一行：Summary 是"当前累积摘要"，不是历史快照序列——
-- 每次重新压缩时整体替换 summary 内容和 covered_until_sequence_no，
-- 不追加新行。UNIQUE 约束（用主键实现）让 UPSERT 有冲突目标。
CREATE TABLE conversation_summaries (
    conversation_id           uuid        PRIMARY KEY
                              REFERENCES conversations(id) ON DELETE CASCADE,
    summary                   text        NOT NULL,

    -- covered_until_sequence_no 记录"这份摘要已经吸收了到哪条消息为止"。
    -- ctxmgr.Request.RecentMessages 只需要传 sequence_no 大于这个值的
    -- 历史消息——重新压缩时不需要把已经摘要过的部分再喂给 LLM 一次。
    covered_until_sequence_no bigint      NOT NULL,
    updated_at                timestamptz NOT NULL DEFAULT now()
);
