CREATE TABLE IF NOT EXISTS recordings (
    id UUID PRIMARY KEY,
    user_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('created', 'uploaded', 'transcribing', 'done', 'failed')),
    object_key TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL CHECK (duration_ms > 0),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    content_type TEXT NOT NULL,
    transcript TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_recordings_user_created
    ON recordings (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS transcription_outbox (
    id UUID PRIMARY KEY,
    recording_id UUID NOT NULL UNIQUE REFERENCES recordings(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL UNIQUE,
    request_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'dispatching', 'dispatched')),
    claimed_at TIMESTAMPTZ,
    dispatched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_transcription_outbox_pending
    ON transcription_outbox (status, created_at);
