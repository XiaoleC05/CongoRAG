-- 0016_hybrid_search
--
-- 参赛版 M2：混合检索的"词法"这一路。
--
-- 【为什么存 search_text 而不是直接 to_tsvector('simple', content)】
-- Postgres 的 simple 配置按非字母字符切词：英文没问题，中文整段会变成
-- 一个巨大的"词"，"过拟合"永远匹配不到正文。Go 侧用 internal/tokenize
-- 预先把中文切成单字 + 双字 bigram、英文切词小写，再拼成空格分隔串落进
-- search_text；tsvector 由生成列算出来。文档和查询用同一套切分规则，
-- 中文子串匹配才成立。
--
-- 【生成列 + GIN 索引】ts_rank_cd 要能走索引过滤，必须给 tsvector 建 GIN。
-- 生成列在写入时自动算，不需要应用层维护两份文本。
--
-- 【已有数据要重建索引】旧行 search_text 是默认空串，词法检索命中不到；
-- 迁移后对文档执行一次重新索引（POST /api/v1/documents/{id}/reindex）。
ALTER TABLE document_chunks
    ADD COLUMN search_text text NOT NULL DEFAULT '',
    ADD COLUMN search_tsv tsvector
        GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED;

CREATE INDEX document_chunks_search_tsv_idx
    ON document_chunks USING GIN (search_tsv);
