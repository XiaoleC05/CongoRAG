-- 0016_hybrid_search down
DROP INDEX IF EXISTS document_chunks_search_tsv_idx;

ALTER TABLE document_chunks
    DROP COLUMN IF EXISTS search_tsv,
    DROP COLUMN IF EXISTS search_text;
