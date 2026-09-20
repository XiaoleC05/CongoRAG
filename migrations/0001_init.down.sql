-- 0001_init 的回滚。
--
-- 顺序必须和 up 严格相反。
-- up   先建被引用的：knowledge_bases → documents → document_chunks
-- down 先删引用的：  memories / document_chunks → documents → knowledge_bases
--
-- 反过来写会报错，因为还有外键指着它，数据库不让你删。
-- IF EXISTS 保证重复执行不报错。

DROP TABLE IF EXISTS memories;
DROP TABLE IF EXISTS document_chunks;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS knowledge_bases;

-- 扩展不删。
-- 严格对称的话这里应该写 DROP EXTENSION IF EXISTS vector，但不这么做：
--   1. 扩展是库级别的资源，一个库里可能有别的东西也在用
--   2. 建表用的是 CREATE EXTENSION IF NOT EXISTS，重复执行本来就安全
--   所以留着它，不影响回滚的语义（表都没了）。
