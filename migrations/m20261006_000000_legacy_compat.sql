-- Older volumes have BIGSERIAL users and lack is_active. Preserve IDs and
-- foreign keys; only add the column required by the current application.
-- This runs before the baseline's partial index referencing is_active.
DO $$
BEGIN
    IF to_regclass('users') IS NOT NULL THEN
        ALTER TABLE users ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE;
    END IF;
END $$;
