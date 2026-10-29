-- 0019：arXiv 推荐的赞/踩反馈（赛题模块四的"推荐互动"）。
--
-- 【为什么只存反馈、不存推荐本身】推荐是"按当前库 + arXiv 最新结果实时算"的，
-- 存下来就要处理过期与失效；用户明确表达过的赞/踩才是需要跨请求保留的状态。
CREATE TABLE IF NOT EXISTS arxiv_feedback (
    knowledge_base_id uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    arxiv_id         text NOT NULL,
    vote             text NOT NULL CHECK (vote IN ('up', 'down')),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (knowledge_base_id, arxiv_id)
);

CREATE INDEX IF NOT EXISTS arxiv_feedback_kb_idx ON arxiv_feedback (knowledge_base_id);
