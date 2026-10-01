-- Transition Codex turn-state storage to a plaintext column.
-- Existing ciphertext is retained temporarily for the one-time application-level
-- backfill; it is nulled after successful decryption and verification.
ALTER TABLE codex_turn_states
    ADD COLUMN IF NOT EXISTS state_plaintext TEXT;

ALTER TABLE codex_turn_states
    ALTER COLUMN state_ciphertext DROP NOT NULL;
