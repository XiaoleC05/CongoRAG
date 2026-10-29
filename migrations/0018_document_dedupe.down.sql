DROP INDEX IF EXISTS documents_fingerprint_by_kb;

ALTER TABLE documents
    DROP COLUMN IF EXISTS duplicate_distance,
    DROP COLUMN IF EXISTS duplicate_of,
    DROP COLUMN IF EXISTS content_fingerprint;
