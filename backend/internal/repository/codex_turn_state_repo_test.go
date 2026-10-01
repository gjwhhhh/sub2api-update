package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type codexTurnStateTestEncryptor struct{}

func (codexTurnStateTestEncryptor) Encrypt(value string) (string, error) {
	return "cipher:" + value, nil
}

func (codexTurnStateTestEncryptor) Decrypt(value string) (string, error) {
	return value, nil
}

func TestCreateCodexTurnStatePersistsEncryptedSidecar(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	state := "turn-state-value"
	digest := sha256.Sum256([]byte(state))
	requestID := "request-1"
	upstreamRequestID := "upstream-1"
	sessionID := "session-1"
	log := &service.UsageLog{
		ID:                77,
		AccountID:         3,
		APIKeyID:          4,
		RequestID:         requestID,
		UpstreamRequestID: &upstreamRequestID,
		SessionID:         &sessionID,
		Model:             "gpt-5.6-sol",
		CodexTurnState: &service.CodexTurnStateSnapshot{
			Value:     state,
			Transport: "ws",
		},
	}

	mock.ExpectExec("INSERT INTO codex_turn_states").
		WithArgs(
			int64(77), int64(3), int64(4), requestID, upstreamRequestID,
			sessionID, "gpt-5.6-sol", "ws", "cipher:"+state, len(state),
			hex.EncodeToString(digest[:]),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	repo := newUsageLogRepositoryWithSQLAndEncryptor(nil, db, codexTurnStateTestEncryptor{})
	require.NoError(t, repo.CreateCodexTurnState(context.Background(), log))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateCodexTurnStateSkipsEmptySnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newUsageLogRepositoryWithSQLAndEncryptor(nil, db, codexTurnStateTestEncryptor{})
	require.NoError(t, repo.CreateCodexTurnState(context.Background(), &service.UsageLog{}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevealCodexTurnStateDecryptsAndReturnsMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	created := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	mock.ExpectQuery("SELECT usage_log_id, state_ciphertext, state_length").
		WithArgs(int64(77)).
		WillReturnRows(sqlmock.NewRows([]string{"usage_log_id", "state_ciphertext", "state_length", "state_sha256", "transport", "created_at"}).
			AddRow(int64(77), "cipher:turn-state-value", 23, "digest", "sse", created))
	repo := newUsageLogRepositoryWithSQLAndEncryptor(nil, db, codexTurnStateTestEncryptor{})
	state, meta, err := repo.RevealCodexTurnState(context.Background(), 77)
	require.NoError(t, err)
	require.Equal(t, "cipher:turn-state-value", state)
	require.Equal(t, int64(77), meta.UsageLogID)
	require.Equal(t, 23, meta.Length)
	require.NoError(t, mock.ExpectationsWereMet())
}
