-- Idempotency keys are scoped by (tenant, user, key) only; mode no longer
-- participates. Keep the oldest row per key and clear the key on the others so
-- the narrower unique index can be built.
WITH ranked AS (
    SELECT id, row_number() OVER (
        PARTITION BY tenant_id, user_id, idempotency_key ORDER BY created_at, id) AS rn
    FROM submissions WHERE idempotency_key IS NOT NULL)
UPDATE submissions s SET idempotency_key = NULL FROM ranked r WHERE s.id = r.id AND r.rn > 1;

-- The old 4-column index (idx_submissions_idempotency) is intentionally left in
-- place: old pods still use it as their ON CONFLICT target. Migration 007
-- removes it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_submissions_idempotency_v2
    ON submissions (tenant_id, user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
