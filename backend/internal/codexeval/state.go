package codexeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
)

const historySchemaVersion = 3

type persistedState struct {
	SchemaVersion int                            `json:"schema_version"`
	Results       map[string][]candyResult       `json:"results"`
	Fingerprints  map[string][]fingerprintResult `json:"fingerprints"`
	Pelicans      map[string][]pelicanResult     `json:"pelicans,omitempty"`
	SIWC          map[string][]siwcResult        `json:"siwc,omitempty"`
}

var (
	statePath      string
	loaded         bool
	stateLoadError error
	storageError   string
)

func loadState() {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return
	}
	loaded = true
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state *struct {
		SchemaVersion json.RawMessage            `json:"schema_version"`
		Results       map[string][]candyResult   `json:"results"`
		Fingerprints  json.RawMessage            `json:"fingerprints"`
		Pelicans      map[string][]pelicanResult `json:"pelicans"`
		SIWC          map[string][]siwcResult    `json:"siwc"`
	}
	var fingerprints map[string][]fingerprintResult
	version := 0
	if err == nil {
		err = json.Unmarshal(data, &state)
		if err == nil && state == nil {
			err = errors.New("测试记录格式无效")
		}
		if err == nil && len(state.SchemaVersion) > 0 {
			var marker *int
			err = json.Unmarshal(state.SchemaVersion, &marker)
			if err == nil && (marker == nil || *marker < 1 || *marker > historySchemaVersion) {
				err = errors.New("测试记录格式无效")
			}
			if err == nil {
				version = *marker
			}
			if err == nil && len(state.Fingerprints) > 0 {
				err = json.Unmarshal(state.Fingerprints, &fingerprints)
			}
		}
	}
	if err != nil {
		stateLoadError = fmt.Errorf("测试记录读取失败，为保护原文件已暂停保存：%w", err)
		storageError = stateLoadError.Error()
		return
	}
	if state.Results != nil {
		candyResults = state.Results
	}
	if fingerprints != nil {
		fingerprintResults = fingerprints
	}
	if state.Pelicans != nil {
		pelicanResults = state.Pelicans
	}
	if state.SIWC != nil {
		siwcResults = state.SIWC
	}
	keepRecent(candyResults)
	keepRecent(fingerprintResults)
	keepRecent(pelicanResults)
	keepRecent(siwcResults)
	if version < historySchemaVersion {
		if version < 2 {
			for id, records := range pelicanResults {
				for i := range records {
					records[i].Variant = "original"
				}
				pelicanResults[id] = records
			}
		}
		backup := filepath.Join(filepath.Dir(statePath), fmt.Sprintf("history.v%d.json", version))
		copy, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err == nil {
			err = copy.Chmod(0600)
			if err == nil {
				_, err = copy.Write(data)
			}
			if closeErr := copy.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			stateLoadError = fmt.Errorf("测试记录备份失败，已暂停保存：%w", err)
			storageError = stateLoadError.Error()
			return
		}
		_ = saveStateLocked()
	}
}

// Each credential keeps its latest historyLimit records of every test.
const historyLimit = 20

func appendRecent[T any](history []T, r T) []T {
	history = append(history, r)
	return history[max(0, len(history)-historyLimit):]
}

func keepRecent[T any](histories map[string][]T) {
	for id, history := range histories {
		histories[id] = history[max(0, len(history)-historyLimit):]
	}
}

func currentState() persistedState {
	return persistedState{SchemaVersion: historySchemaVersion, Results: candyResults, Fingerprints: fingerprintResults, Pelicans: pelicanResults, SIWC: siwcResults}
}

// Call with mu held so all histories are written as one snapshot.
func saveStateLocked() (err error) {
	defer func() {
		storageError = ""
		if err != nil {
			storageError = "测试记录保存失败：" + err.Error()
		}
	}()
	if stateLoadError != nil {
		return stateLoadError
	}
	data, err := json.Marshal(currentState())
	if err != nil {
		return err
	}
	tmp := statePath + ".tmp"
	defer os.Remove(tmp)
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath)
}

func recordResponse(scope string, query url.Values) managementResponse {
	authID, id := query.Get("auth_id"), query.Get("id")
	mu.Lock()
	defer mu.Unlock()
	var record any
	switch scope {
	case "fingerprint":
		if i := slices.IndexFunc(fingerprintResults[authID], func(r fingerprintResult) bool { return r.ID == id }); i >= 0 {
			record = fingerprintResults[authID][i]
		}
	case "pelican":
		if i := slices.IndexFunc(pelicanResults[authID], func(r pelicanResult) bool { return r.ID == id }); i >= 0 {
			record = pelicanResults[authID][i]
		}
	case "siwc":
		if i := slices.IndexFunc(siwcResults[authID], func(r siwcResult) bool { return r.ID == id || r.RunID == id }); i >= 0 {
			record = siwcResults[authID][i]
		}
	}
	if record == nil {
		return jsonError(http.StatusNotFound, "记录不存在，可能已被清空")
	}
	return jsonResponse(http.StatusOK, record)
}

func clearHistoryResponse(scope string) managementResponse {
	mu.Lock()
	defer mu.Unlock()
	previous := currentState()
	switch scope {
	case "fingerprint":
		fingerprintResults = map[string][]fingerprintResult{}
	case "candy":
		candyResults = map[string][]candyResult{}
	case "pelican":
		pelicanResults = map[string][]pelicanResult{}
	case "siwc":
		siwcResults = map[string][]siwcResult{}
	case "all":
		candyResults, fingerprintResults = map[string][]candyResult{}, map[string][]fingerprintResult{}
		pelicanResults = map[string][]pelicanResult{}
		siwcResults = map[string][]siwcResult{}
	default:
		return jsonError(http.StatusBadRequest, "未知测试类型")
	}
	if err := saveStateLocked(); err != nil {
		candyResults, fingerprintResults = previous.Results, previous.Fingerprints
		pelicanResults = previous.Pelicans
		siwcResults = previous.SIWC
		return jsonError(http.StatusInternalServerError, "清空测试记录失败："+err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]bool{"cleared": true})
}
