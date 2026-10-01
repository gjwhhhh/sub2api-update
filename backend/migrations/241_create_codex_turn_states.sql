-- Persist the opaque Codex turn-state returned by OpenAI OAuth accounts.
-- The value is encrypted by the application before it reaches this table.
CREATE TABLE IF NOT EXISTS codex_turn_states (
    id                  BIGSERIAL PRIMARY KEY,
    usage_log_id        BIGINT NOT NULL UNIQUE
                            REFERENCES usage_logs(id) ON DELETE CASCADE,
    account_id          BIGINT NOT NULL
                            REFERENCES accounts(id) ON DELETE CASCADE,
    api_key_id          BIGINT NOT NULL,
    request_id          VARCHAR(64),
    upstream_request_id VARCHAR(128),
    session_id          VARCHAR(255),
    model               VARCHAR(100) NOT NULL,
    transport           VARCHAR(16) NOT NULL,
    state_ciphertext    TEXT NOT NULL,
    state_length        INTEGER NOT NULL CHECK (state_length > 0),
    state_sha256        CHAR(64) NOT NULL,
    encryption_version  SMALLINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_codex_turn_states_account_created
    ON codex_turn_states(account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_codex_turn_states_sha256
    ON codex_turn_states(state_sha256);
