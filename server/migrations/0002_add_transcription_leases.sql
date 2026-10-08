ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS transcribe_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS transcribe_lease_token UUID,
    ADD COLUMN IF NOT EXISTS transcribe_lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_recordings_transcribe_reclaim
    ON recordings (status, transcribe_lease_until)
    WHERE status = 'transcribing';
