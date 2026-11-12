package codexeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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

func (engine *Engine) loadState() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.loaded {
		return
	}
	engine.loaded = true
	data, err := engine.historyStore().Load()
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
		engine.stateLoadError = fmt.Errorf("测试记录读取失败，为保护原文件已暂停保存：%w", err)
		engine.storageError = engine.stateLoadError.Error()
		return
	}
	if state.Results != nil {
		engine.candyResults = state.Results
	}
	if fingerprints != nil {
		engine.fingerprintResults = fingerprints
	}
	if state.Pelicans != nil {
		engine.pelicanResults = state.Pelicans
	}
	if state.SIWC != nil {
		engine.siwcResults = state.SIWC
	}
	keepRecent(engine.candyResults)
	keepRecent(engine.fingerprintResults)
	keepRecent(engine.pelicanResults)
	keepRecent(engine.siwcResults)
	if version < historySchemaVersion {
		if version < 2 {
			for id, records := range engine.pelicanResults {
				for i := range records {
					records[i].Variant = "original"
				}
				engine.pelicanResults[id] = records
			}
		}
		err := engine.historyStore().Backup(version, data)
		if err != nil {
			engine.stateLoadError = fmt.Errorf("测试记录备份失败，已暂停保存：%w", err)
			engine.storageError = engine.stateLoadError.Error()
			return
		}
		_ = engine.saveStateLocked()
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

func (engine *Engine) currentState() persistedState {
	return persistedState{SchemaVersion: historySchemaVersion, Results: engine.candyResults, Fingerprints: engine.fingerprintResults, Pelicans: engine.pelicanResults, SIWC: engine.siwcResults}
}

// Call with mu held so all histories are written as one snapshot.
func (engine *Engine) saveStateLocked() (err error) {
	defer func() {
		engine.storageError = ""
		if err != nil {
			engine.storageError = "测试记录保存失败：" + err.Error()
		}
	}()
	if engine.stateLoadError != nil {
		return engine.stateLoadError
	}
	data, err := json.Marshal(engine.currentState())
	if err != nil {
		return err
	}
	return engine.historyStore().Save(data)
}

func (engine *Engine) recordResponse(scope string, query url.Values) managementResponse {
	authID, id := query.Get("auth_id"), query.Get("id")
	engine.mu.Lock()
	defer engine.mu.Unlock()
	var record any
	switch scope {
	case "fingerprint":
		if i := slices.IndexFunc(engine.fingerprintResults[authID], func(r fingerprintResult) bool { return r.ID == id }); i >= 0 {
			record = engine.fingerprintResults[authID][i]
		}
	case "pelican":
		if i := slices.IndexFunc(engine.pelicanResults[authID], func(r pelicanResult) bool { return r.ID == id }); i >= 0 {
			record = engine.pelicanResults[authID][i]
		}
	case "siwc":
		if i := slices.IndexFunc(engine.siwcResults[authID], func(r siwcResult) bool { return r.ID == id || r.RunID == id }); i >= 0 {
			record = engine.siwcResults[authID][i]
		}
	}
	if record == nil {
		return jsonError(http.StatusNotFound, "记录不存在，可能已被清空")
	}
	return jsonResponse(http.StatusOK, record)
}

func (engine *Engine) clearHistoryResponse(scope string) managementResponse {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	previous := engine.currentState()
	switch scope {
	case "fingerprint":
		engine.fingerprintResults = map[string][]fingerprintResult{}
	case "candy":
		engine.candyResults = map[string][]candyResult{}
	case "pelican":
		engine.pelicanResults = map[string][]pelicanResult{}
	case "siwc":
		engine.siwcResults = map[string][]siwcResult{}
	case "all":
		engine.candyResults, engine.fingerprintResults = map[string][]candyResult{}, map[string][]fingerprintResult{}
		engine.pelicanResults = map[string][]pelicanResult{}
		engine.siwcResults = map[string][]siwcResult{}
	default:
		return jsonError(http.StatusBadRequest, "未知测试类型")
	}
	if err := engine.saveStateLocked(); err != nil {
		engine.candyResults, engine.fingerprintResults = previous.Results, previous.Fingerprints
		engine.pelicanResults = previous.Pelicans
		engine.siwcResults = previous.SIWC
		return jsonError(http.StatusInternalServerError, "清空测试记录失败："+err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]bool{"cleared": true})
}

func (engine *Engine) historyStore() HistoryStore {
	if engine.history != nil {
		return engine.history
	}
	return JSONHistoryStore{Path: engine.statePath}
}
