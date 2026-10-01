-- Indexes for admin usage-log filtering by persisted Codex turn-state metadata.
CREATE INDEX IF NOT EXISTS idx_codex_turn_states_length_created
    ON codex_turn_states(state_length, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_codex_turn_states_transport_created
    ON codex_turn_states(transport, created_at DESC);
