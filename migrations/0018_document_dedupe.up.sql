-- 0018：文档内容指纹与"上传时的语义去重"（赛题模块一）。
--
-- 【为什么要单独存指纹】去重判据是"正文内容"，不是文件名。worker 解析完
-- 一份文档后算一次 SimHash 存在这里，下次上传即可在同库内做汉明距离比较；
-- 不存的话每次都要重读全部分块重新算，541 页的书代价太大。
--
-- 【为什么 duplicate_of 自引用】"这份文档与库内哪一份高度重合"，指回自己
-- 知识库里的另一行。ON DELETE SET NULL 保证删掉旧版本时告警字段被清掉，
-- 不会留下悬空 id。
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS content_fingerprint text,
    ADD COLUMN IF NOT EXISTS duplicate_of uuid REFERENCES documents(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS duplicate_distance integer;

-- 同一知识库内按指纹找人；部分索引避免把 md/txt 这类没有指纹的行也扫进来。
CREATE INDEX IF NOT EXISTS documents_fingerprint_by_kb
    ON documents (knowledge_base_id)
    WHERE content_fingerprint IS NOT NULL;
