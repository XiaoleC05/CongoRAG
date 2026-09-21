-- 回退 0007。
--
-- 【这不是精确还原，要如实说明】up 跑完之后，无从区分「这一位本来就有」
-- 与「这一位是 up 加上去的」，所以回退时一律摘掉。
--
-- 代价可以接受：回退意味着代码也退回「不读这一位」的版本（那正是这条迁移
-- 存在的前提），行为完全等价。重新 up 一次即可恢复。
UPDATE llm_models
SET capabilities = array_remove(capabilities, 'tool_calling')
WHERE kind = 'chat';
