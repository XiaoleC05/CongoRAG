-- 0015_pdf_blocks
--
-- 参赛版「科研助手」的 PDF 学术解析：给文档与分块补上"学术结构"字段。
--
-- 【为什么加正式列而不是塞 metadata jsonb】
--   document_chunks.metadata 在 0001 里就预留了，但它是给"暂时不知道形状"
--   的附加信息用的。页码/坐标/章节会参与查询（按文档+页码列出 blocks、
--   引用跳转、评测统计），塞进 jsonb 后要么没法走索引、要么每次查询都要
--   在 SQL 里拆 JSON——所以这里给它们正式列。
--
-- 【page_start/page_end 可空】md/txt 文档没有页的概念，保持 NULL 以区分
--   "没有页码"和"第 0 页"（后者是非法值）。
--
-- 【bbox 归一化 0~1、左上原点】见 _dev/CONTRACT_pdf-service.md：全系统只
--   保留这一种坐标系，前端 PDF.js 叠层直接按百分比定位，不用处理缩放/旋转。
ALTER TABLE documents
    ADD COLUMN page_count int NOT NULL DEFAULT 0,
    ADD COLUMN parser     text NOT NULL DEFAULT '';

ALTER TABLE document_chunks
    ADD COLUMN page_start  int,
    ADD COLUMN page_end    int,
    ADD COLUMN bbox        jsonb,
    ADD COLUMN section_path jsonb,
    ADD COLUMN block_type  text NOT NULL DEFAULT 'paragraph';

-- 按文档+页码取 blocks（阅读器大纲/跳转、引用相邻块扩展）会用到。
CREATE INDEX document_chunks_document_id_page_idx
    ON document_chunks (document_id, page_start);
