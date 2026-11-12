package codexeval

import (
	"fmt"
	"os"
	"path/filepath"
)

// HistoryStore persists a complete snapshot and preserves old schema bytes before
// migration. Implementations must replace a snapshot atomically.
type HistoryStore interface {
	Load() ([]byte, error)
	Save([]byte) error
	Backup(version int, data []byte) error
}

type JSONHistoryStore struct{ Path string }

func (s JSONHistoryStore) Load() ([]byte, error) { return os.ReadFile(s.Path) }
func (s JSONHistoryStore) Save(data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".history-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}
func (s JSONHistoryStore) Backup(version int, data []byte) error {
	path := filepath.Join(filepath.Dir(s.Path), fmt.Sprintf("history.v%d.json", version))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
