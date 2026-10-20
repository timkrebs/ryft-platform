-- The admin user view (docs/DECISIONS.md D89): what a device's last accepted sync
-- carried, and when a push token last reached its device.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_sync_summary jsonb
    CHECK (last_sync_summary IS NULL OR jsonb_typeof(last_sync_summary) = 'object');
ALTER TABLE push_tokens ADD COLUMN IF NOT EXISTS last_success_at timestamptz;

-- notify records a delivered push on its token, nothing else.
GRANT UPDATE (last_success_at) ON push_tokens TO ryft_notify;
