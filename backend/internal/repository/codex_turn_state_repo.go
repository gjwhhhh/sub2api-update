package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CreateCodexTurnState encrypts and stores the turn-state sidecar associated
// with a usage row. The lookup by request_id keeps this compatible with the
// existing best-effort usage-log batch writer, which does not return IDs.
func (r *usageLogRepository) CreateCodexTurnState(ctx context.Context, log *service.UsageLog) error {
	if r == nil || log == nil || log.CodexTurnState == nil || strings.TrimSpace(log.CodexTurnState.Value) == "" {
		return nil
	}
	if r.turnStateEncryptor == nil {
		return errors.New("codex turn-state encryption is not configured")
	}
	if r.sql == nil {
		return errors.New("codex turn-state database is not configured")
	}
	state := strings.TrimSpace(log.CodexTurnState.Value)
	usageLogID := log.ID
	if usageLogID == 0 {
		if strings.TrimSpace(log.RequestID) == "" {
			return errors.New("codex turn-state requires usage log id or request id")
		}
		if err := scanSingleRow(ctx, r.sql,
			"SELECT id FROM usage_logs WHERE request_id = $1 AND api_key_id = $2",
			[]any{log.RequestID, log.APIKeyID}, &usageLogID); err != nil {
			return fmt.Errorf("resolve usage log for codex turn-state: %w", err)
		}
	}
	ciphertext, err := r.turnStateEncryptor.Encrypt(state)
	if err != nil {
		return fmt.Errorf("encrypt codex turn-state: %w", err)
	}
	digest := sha256.Sum256([]byte(state))
	requestID := strings.TrimSpace(log.RequestID)
	var requestIDArg any
	if requestID != "" {
		requestIDArg = requestID
	}
	var upstreamRequestID any
	if log.UpstreamRequestID != nil && strings.TrimSpace(*log.UpstreamRequestID) != "" {
		upstreamRequestID = strings.TrimSpace(*log.UpstreamRequestID)
	}
	var sessionID any
	if log.SessionID != nil && strings.TrimSpace(*log.SessionID) != "" {
		sessionID = strings.TrimSpace(*log.SessionID)
	}
	transport := strings.TrimSpace(log.CodexTurnState.Transport)
	if transport == "" {
		transport = "unknown"
	}
	_, err = r.sql.ExecContext(ctx, `
		INSERT INTO codex_turn_states
			(usage_log_id, account_id, api_key_id, request_id, upstream_request_id,
			 session_id, model, transport, state_ciphertext, state_length,
			 state_sha256, encryption_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1)
		ON CONFLICT (usage_log_id) DO NOTHING
	`, usageLogID, log.AccountID, log.APIKeyID, requestIDArg, upstreamRequestID,
		sessionID, log.Model, transport, ciphertext, len([]byte(state)), hex.EncodeToString(digest[:]))
	if err != nil {
		return fmt.Errorf("persist codex turn-state: %w", err)
	}
	return nil
}
