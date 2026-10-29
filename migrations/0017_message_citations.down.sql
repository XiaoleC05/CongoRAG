-- 0017_message_citations down
ALTER TABLE messages
    DROP COLUMN IF EXISTS grounding,
    DROP COLUMN IF EXISTS citations;
