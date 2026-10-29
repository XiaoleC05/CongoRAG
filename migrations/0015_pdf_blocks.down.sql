-- 0015_pdf_blocks down
DROP INDEX IF EXISTS document_chunks_document_id_page_idx;

ALTER TABLE document_chunks
    DROP COLUMN IF EXISTS block_type,
    DROP COLUMN IF EXISTS section_path,
    DROP COLUMN IF EXISTS bbox,
    DROP COLUMN IF EXISTS page_end,
    DROP COLUMN IF EXISTS page_start;

ALTER TABLE documents
    DROP COLUMN IF EXISTS parser,
    DROP COLUMN IF EXISTS page_count;
