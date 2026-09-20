-- 0002_llm_config 的回滚。
--
-- 顺序和 up 相反：先删引用的（llm_models），再删被引用的（llm_providers）。
DROP TABLE IF EXISTS llm_models;
DROP TABLE IF EXISTS llm_providers;
