package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Only metadata is selected here; the ciphertext never enters the read API.
func (r *usageLogRepository) GetCodexTurnStateMetadata(ctx context.Context, usageLogID int64) (*service.CodexTurnStateMetadata, error) {
	var meta service.CodexTurnStateMetadata
	err := scanSingleRow(ctx, r.sql, `SELECT usage_log_id, state_length, state_sha256, transport, created_at
		FROM codex_turn_states WHERE usage_log_id = $1`, []any{usageLogID},
		&meta.UsageLogID, &meta.Length, &meta.SHA256, &meta.Transport, &meta.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *usageLogRepository) RevealCodexTurnState(ctx context.Context, usageLogID int64) (string, *service.CodexTurnStateMetadata, error) {
	if r == nil || r.sql == nil || r.turnStateEncryptor == nil {
		return "", nil, errors.New("codex turn-state encryption is not configured")
	}
	decryptor, ok := r.turnStateEncryptor.(interface{ Decrypt(string) (string, error) })
	if !ok {
		return "", nil, errors.New("codex turn-state decryptor is not configured")
	}
	var ciphertext string
	var meta service.CodexTurnStateMetadata
	err := scanSingleRow(ctx, r.sql, `SELECT usage_log_id, state_ciphertext, state_length, state_sha256, transport, created_at FROM codex_turn_states WHERE usage_log_id = $1`, []any{usageLogID}, &meta.UsageLogID, &ciphertext, &meta.Length, &meta.SHA256, &meta.Transport, &meta.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	state, err := decryptor.Decrypt(ciphertext)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt codex turn-state: %w", err)
	}
	if len([]byte(state)) != meta.Length {
		return "", nil, errors.New("codex turn-state length mismatch")
	}
	return state, &meta, nil
}

func (r *usageLogRepository) hydrateCodexTurnStateMetadata(ctx context.Context, logs []service.UsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	placeholders := make([]string, len(logs))
	args := make([]any, len(logs))
	byID := make(map[int64]*service.UsageLog, len(logs))
	for i := range logs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = logs[i].ID
		byID[logs[i].ID] = &logs[i]
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT usage_log_id, state_length, state_sha256, transport, created_at
		FROM codex_turn_states WHERE usage_log_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var meta service.CodexTurnStateMetadata
		if err := rows.Scan(&meta.UsageLogID, &meta.Length, &meta.SHA256, &meta.Transport, &meta.CreatedAt); err != nil {
			return err
		}
		if log := byID[meta.UsageLogID]; log != nil {
			log.CodexTurnStateMetadata = &meta
		}
	}
	return rows.Err()
}

func appendCodexTurnStateWhereCondition(conditions []string, args []any, filter usagestats.CodexTurnStateFilter, alias string) ([]string, []any) {
	if !filter.Active() {
		return conditions, args
	}
	if alias == "" {
		alias = "usage_logs"
	}
	subquery := "SELECT 1 FROM codex_turn_states cts WHERE cts.usage_log_id = " + alias + ".id"
	if filter.Present != nil && !*filter.Present {
		return append(conditions, "NOT EXISTS ("+subquery+")"), args
	}
	if filter.Length != nil {
		subquery += fmt.Sprintf(" AND cts.state_length = $%d", len(args)+1)
		args = append(args, *filter.Length)
	}
	if filter.Transport != "" {
		subquery += fmt.Sprintf(" AND cts.transport = $%d", len(args)+1)
		args = append(args, filter.Transport)
	}
	return append(conditions, "EXISTS ("+subquery+")"), args
}

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
