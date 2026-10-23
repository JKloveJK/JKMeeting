CREATE TABLE IF NOT EXISTS transcode_jobs (
    recording_id UUID PRIMARY KEY REFERENCES recordings(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'ready', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    master_key TEXT,
    variants TEXT,
    transcode_ms INTEGER,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS transcode_outbox (
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

CREATE INDEX IF NOT EXISTS idx_transcode_outbox_pending
    ON transcode_outbox (status, created_at);
