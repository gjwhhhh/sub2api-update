package codexeval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// One evaluation instance belongs to the native server. Account credentials remain
// inside AccountTestService; this package receives only a name and a direct sender.
type Target struct {
	GroupID   int64 `json:"group_id"`
	AccountID int64 `json:"account_id"`
}
type Info struct {
	Target
	AccountName string `json:"account_name"`
	RunID       string `json:"run_id"`
}
type credential struct {
	ID     string
	Target Target
	Name   string
	runID  string
	ctx    context.Context
}

func (a credential) info() Info { return Info{a.Target, a.Name, a.runID} }

type Sender func(context.Context, int64, []byte, http.Header) (*http.Response, error)
type Resolver func(context.Context, int64) (string, error)

var (
	mu      sync.Mutex
	sender  Sender
	resolve Resolver
	active  = map[string]*run{}
)

type run struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Target   Target `json:"target"`
	Status   string `json:"status"`
	Progress any    `json:"progress"`
	cancel   context.CancelFunc
}

func Initialize(dataDir string, send Sender, lookup Resolver) {
	sender, resolve = send, lookup
	statePath = filepath.Join(dataDir, "codex-eval", "history.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		storageError = err.Error()
	}
	loadState()
}

type StartRequest struct {
	Type        string   `json:"type"`
	Targets     []Target `json:"targets"`
	Model       string   `json:"model"`
	Effort      string   `json:"effort"`
	Runs        int      `json:"runs"`
	Concurrency int      `json:"concurrency"`
	Variant     string   `json:"variant"`
}
type unavailable struct {
	Target Target `json:"target"`
	Error  string `json:"error"`
}
type StartSummary struct {
	Started     []run         `json:"started"`
	Busy        []Target      `json:"busy"`
	Unavailable []unavailable `json:"unavailable"`
}

func Start(ctx context.Context, req StartRequest) (StartSummary, error) {
	return start(ctx, req, false)
}

func start(ctx context.Context, req StartRequest, inspection bool) (StartSummary, error) {
	return startChecked(ctx, req, inspection, nil)
}

func startChecked(ctx context.Context, req StartRequest, inspection bool, check func(context.Context, int64) error) (StartSummary, error) {
	summary := StartSummary{Started: []run{}, Busy: []Target{}, Unavailable: []unavailable{}}
	req.Model = strings.TrimSpace(req.Model)
	req.Effort = strings.TrimSpace(req.Effort)
	if req.Type == "siwc" && req.Model == "" {
		req.Model = "gpt-6.1-sol"
	}
	if !validModelName(req.Model, req.Type == "candy" || req.Type == "pelican") {
		return summary, fmt.Errorf("请选择有效模型")
	}
	if len(req.Targets) == 0 {
		return summary, fmt.Errorf("请选择账户")
	}
	var variant pelicanVariant
	switch req.Type {
	case "candy":
		req.Runs = min(max(req.Runs, 1), candyMaxRuns)
	case "pelican":
		if req.Variant == "" {
			req.Variant = "original"
		}
		var ok bool
		variant, ok = findPelicanVariant(req.Variant)
		if !ok {
			return summary, fmt.Errorf("请选择有效版本")
		}
	case "fingerprint":
		if req.Concurrency == 0 {
			req.Concurrency = fingerprintDefaultConcurrency
		}
		if req.Concurrency < 1 || req.Concurrency > fingerprintMaxConcurrency {
			return summary, fmt.Errorf("每账户并发须为 1–%d", fingerprintMaxConcurrency)
		}
	case "siwc":
	default:
		return summary, fmt.Errorf("未知测试类型")
	}
	seen := map[int64]bool{}
	for _, target := range req.Targets {
		if seen[target.AccountID] {
			continue
		}
		seen[target.AccountID] = true
		if target.AccountID <= 0 || (!inspection && target.GroupID <= 0) {
			summary.Unavailable = append(summary.Unavailable, unavailable{target, "账户与分组 ID 必须为正整数"})
			continue
		}
		name, err := resolve(ctx, target.AccountID)
		if err != nil {
			summary.Unavailable = append(summary.Unavailable, unavailable{target, err.Error()})
			continue
		}
		id := strconv.FormatInt(target.AccountID, 10)
		mu.Lock()
		if active[id] != nil {
			summary.Busy = append(summary.Busy, target)
			mu.Unlock()
			continue
		}
		parent := context.Background()
		if inspection {
			parent = ctx
		}
		runCtx, cancel := context.WithCancel(parent)
		a := credential{id, target, name, uuidV7().String(), runCtx}
		r := &run{ID: a.runID, Type: req.Type, Target: target, Status: "running"}
		r.cancel = cancel
		active[id] = r
		switch req.Type {
		case "candy":
			candyRunning[id] = &candyProgress{Total: req.Runs}
			r.Progress = candyRunning[id]
		case "pelican":
			pelicanRunning[id] = true
		case "fingerprint":
			fingerprintRunning[id] = &fingerprintProgress{Model: req.Model, Total: fingerprintTarget, Concurrency: req.Concurrency}
			r.Progress = fingerprintRunning[id]
		case "siwc":
			siwcRunning[id] = true
		}
		// Start replies expose identity; state serializes progress under the lock.
		copyRun := *r
		copyRun.Progress = nil
		copyRun.cancel = nil
		summary.Started = append(summary.Started, copyRun)
		fpProgress := fingerprintRunning[id]
		mu.Unlock()
		go func() {
			defer func() { cancel(); mu.Lock(); delete(active, id); mu.Unlock() }()
			var beforeSend func() error
			if check != nil {
				beforeSend = func() error { return check(runCtx, target.AccountID) }
			}
			switch req.Type {
			case "candy":
				runCandyChecked(a, req.Model, req.Effort, req.Runs, beforeSend)
			case "pelican":
				runPelican(a, req.Model, req.Effort, variant)
			case "fingerprint":
				runFingerprintChecked(runCtx, a, req.Model, fpProgress, beforeSend)
			case "siwc":
				runSIWC(a, req.Model)
			}
		}()
	}
	return summary, nil
}
func Cancel(id string) bool {
	mu.Lock()
	defer mu.Unlock()
	for _, r := range active {
		if r.ID == id {
			r.Status = "cancelling"
			r.cancel()
			return true
		}
	}
	return false
}
func State() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	runs := make([]*run, 0, len(active))
	for _, r := range active {
		runs = append(runs, r)
	}
	fp := map[string][]fingerprintResult{}
	pl := map[string][]pelicanResult{}
	sw := map[string][]siwcResult{}
	for id, rs := range fingerprintResults {
		fp[id] = summaries(rs)
	}
	for id, rs := range pelicanResults {
		pl[id] = summaries(rs)
	}
	for id, rs := range siwcResults {
		sw[id] = summaries(rs)
	}
	raw, err := json.Marshal(map[string]any{"active": runs, "candy": candyResults, "fingerprint": fp, "pelican": pl, "siwc": sw, "storage_error": storageError})
	return raw, err
}
func Record(kind string, accountID int64, id string) ([]byte, int) {
	r := recordResponse(kind, url.Values{"auth_id": {strconv.FormatInt(accountID, 10)}, "id": {id}})
	if r.StatusCode != 200 {
		return r.Body, r.StatusCode
	}
	var record json.RawMessage = r.Body
	raw, _ := json.Marshal(map[string]any{"type": kind, kind: record})
	return raw, 200
}
func Clear(scope string) ([]byte, int) { r := clearHistoryResponse(scope); return r.Body, r.StatusCode }
func failedStatus(ctx context.Context) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "failed"
}
func summaries[T interface{ summary() T }](rs []T) []T {
	out := make([]T, len(rs))
	for i, r := range rs {
		out[i] = r.summary()
	}
	return out
}
func truncate(text string, limit int) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return string(r)
}

type managementResponse struct {
	StatusCode int
	Body       []byte
}

func jsonResponse(status int, v any) managementResponse {
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonError(500, err.Error())
	}
	return managementResponse{status, raw}
}
func jsonError(status int, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": map[string]string{"message": message}})
}
