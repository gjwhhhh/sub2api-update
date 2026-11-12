// sub2api-package: backend/internal/codexeval
package codexeval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		idle := len(active) == 0
		mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("workers did not exit")
}
func TestCodexEvalRuntime(t *testing.T) {
	mu.Lock()
	loaded = false
	stateLoadError = nil
	storageError = ""
	candyResults = map[string][]candyResult{}
	fingerprintResults = map[string][]fingerprintResult{}
	pelicanResults = map[string][]pelicanResult{}
	siwcResults = map[string][]siwcResult{}
	mu.Unlock()
	oldDelay := retryDelay
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = oldDelay })
	started := make(chan struct{})
	block := false
	pelicanAnswer := "<html><svg></svg></html>"
	var fingerprintCalls atomic.Int32
	var resolveCalls, supplierCalls atomic.Int32
	var lastPrompt string
	send := func(ctx context.Context, id int64, body []byte, headers http.Header) (*http.Response, error) {
		supplierCalls.Add(1)
		if id != 7 {
			t.Errorf("sent to unexpected account %d", id)
		}
		if block {
			select {
			case started <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		answer := "21"
		if input, ok := req["input"].([]any); ok && len(input) > 0 {
			item, _ := input[len(input)-1].(map[string]any)
			var last string
			if contentList, ok := item["content"].([]any); ok && len(contentList) > 0 {
				if first, ok := contentList[0].(map[string]any); ok {
					last, _ = first["text"].(string)
				}
			} else if text, ok := item["content"].(string); ok {
				last = text
			}
			if strings.Contains(last, "HTML") || strings.Contains(last, "完整的 SVG") {
				lastPrompt = last
				answer = pelicanAnswer
			} else if strings.Contains(last, "355") {
				answer = strings.Repeat("42,", 320)
				if fingerprintCalls.Add(1) == 1 {
					answer = "cannot answer"
				}
			} else if strings.Contains(last, "Reply with OK.") {
				answer = "OK"
			}
		}
		data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}, "usage": map[string]int{"input_tokens": 2, "output_tokens": 3}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(data) + "\n\n"))}, nil
	}
	Initialize(t.TempDir(), send, func(_ context.Context, id int64) (string, error) {
		resolveCalls.Add(1)
		if id != 7 {
			return "", fmt.Errorf("missing account %d", id)
		}
		return "selected", nil
	})
	if FingerprintModelID("openai/gpt-6.1-sol") != "gpt-6-astra" {
		t.Fatalf("unexpected FingerprintModelID alias resolution: %s", FingerprintModelID("openai/gpt-6.1-sol"))
	}
	for _, kind := range []string{"candy", "fingerprint", "pelican", "siwc"} {
		request := StartRequest{Type: kind, Targets: []Target{{1, 7}, {1, 7}, {1, 8}}, Model: "fixture", Effort: "none", Runs: 2}
		summary, err := Start(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(summary.Started) != 1 || len(summary.Unavailable) != 1 || len(summary.Busy) != 0 {
			t.Fatalf("bad admission: %+v", summary)
		}
		waitIdle(t)
	}
	mu.Lock()
	if len(candyResults["7"]) != 2 || !candyResults["7"][0].OK || candyResults["7"][0].GroupID != 1 || candyResults["7"][0].Effort != "none" {
		t.Errorf("candy: %+v", candyResults)
	}
	if len(fingerprintResults["7"]) != 1 || len(fingerprintResults["7"][0].Samples) != 3 || fingerprintResults["7"][0].Status != "completed" || fingerprintCalls.Load() != 4 || fingerprintResults["7"][0].InputTokens != 8 {
		t.Errorf("fingerprint: %+v", fingerprintResults)
	}
	if len(pelicanResults["7"]) != 1 || pelicanResults["7"][0].HTML == "" || pelicanResults["7"][0].Variant != "original" || lastPrompt != pelicanVariants[0].Prompt {
		t.Errorf("pelican: %+v", pelicanResults)
	}
	if len(siwcResults["7"]) != 1 || siwcResults["7"][0].Status != "available" || siwcResults["7"][0].Model != "fixture" || siwcResults["7"][0].InputTokens != 2 || siwcResults["7"][0].OutputTokens != 3 {
		t.Errorf("siwc: %+v", siwcResults)
	}
	sw := siwcResults["7"][0]
	mu.Unlock()
	if detail, status := Record("siwc", 7, sw.ID); status != 200 || !strings.Contains(string(detail), `"available"`) || !strings.Contains(string(detail), `"model":"fixture"`) {
		t.Fatalf("siwc detail: %d %s", status, detail)
	}
	resolved, sent := resolveCalls.Load(), supplierCalls.Load()
	if _, err := Start(context.Background(), StartRequest{Type: "pelican", Targets: []Target{{1, 7}}, Model: "fixture", Variant: "unknown"}); err == nil || resolveCalls.Load() != resolved || supplierCalls.Load() != sent {
		t.Fatal("unknown version reached account resolution or supplier")
	}
	pelicanAnswer = "```html\n<html>fallback</html>\n```\n```SVG\n<svg>composite</svg>\n```"
	if _, err := Start(context.Background(), StartRequest{Type: "pelican", Targets: []Target{{1, 7}}, Model: "fixture", Variant: "all-in-one"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	composite := pelicanResults["7"][1]
	if composite.Variant != "all-in-one" || !strings.HasSuffix(composite.HTML, "<svg>composite</svg>") || lastPrompt != pelicanVariants[1].Prompt {
		t.Fatalf("composite routing: %+v", composite)
	}
	state, _ := State()
	if !strings.Contains(string(state), `"variant":"all-in-one"`) || strings.Contains(string(state), `"html":`) {
		t.Fatalf("version summary: %s", state)
	}
	if detail, status := Record("pelican", 7, composite.ID); status != 200 || !strings.Contains(string(detail), `"variant":"all-in-one"`) || !strings.Contains(string(detail), `"html":`) {
		t.Fatalf("version detail: %s", detail)
	}
	fp := fingerprintResults["7"][0]
	if fp.summary().Samples != nil || len(fp.Samples) != 3 {
		t.Fatal("summary changed stored samples")
	}
	if detail, status := Record("fingerprint", 7, fp.ID); status != 200 || !strings.Contains(string(detail), `"samples":[`) {
		t.Fatalf("fingerprint detail: %s", detail)
	}
	for _, answer := range []string{"No HTML in this answer", "<html>" + strings.Repeat("x", pelicanMaxCode) + "</html>"} {
		pelicanAnswer = answer
		result := evaluatePelican(credential{ID: "7", Target: Target{1, 7}, Name: "selected", ctx: context.Background()}, "fixture", "none", pelicanVariants[0])
		if result.Status != "failed" || result.Error == "" || result.HTML != "" || result.Variant != "original" {
			t.Fatalf("invalid HTML reported completed: %+v", result)
		}
	}
	block = true
	summary, err := Start(context.Background(), StartRequest{Type: "candy", Targets: []Target{{2, 7}}, Model: "fixture", Runs: 3})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sender not started")
	}
	busy, err := Start(context.Background(), StartRequest{Type: "pelican", Targets: []Target{{1, 7}}, Model: "fixture"})
	if err != nil || len(busy.Busy) != 1 {
		t.Fatalf("cross-type busy failed: %+v %v", busy, err)
	}
	if !Cancel(summary.Started[0].ID) {
		t.Fatal("cancel not accepted")
	}
	waitIdle(t)
	mu.Lock()
	last := candyResults["7"][len(candyResults["7"])-1]
	mu.Unlock()
	if last.Status != "cancelled" || last.OK {
		t.Fatalf("cancel saved as success: %+v", last)
	}
	summary, err = Start(context.Background(), StartRequest{Type: "fingerprint", Targets: []Target{{2, 7}}, Model: "fixture", Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fingerprint sender not started")
	}
	if !Cancel(summary.Started[0].ID) {
		t.Fatal("fingerprint cancel not accepted")
	}
	waitIdle(t)
	if latest := fingerprintResults["7"][1]; latest.Status != "cancelled" || len(latest.Samples) != 1 {
		t.Fatalf("fingerprint cancellation: %+v", latest)
	}
	summary, err = Start(context.Background(), StartRequest{Type: "pelican", Targets: []Target{{1, 7}}, Model: "fixture", Variant: "all-in-one"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Pelican sender not started")
	}
	Cancel(summary.Started[0].ID)
	waitIdle(t)
	if latest := pelicanResults["7"][2]; latest.Status != "cancelled" || latest.Variant != "all-in-one" {
		t.Fatalf("Pelican cancellation: %+v", latest)
	}
	// Exercise the same original atomic history path, including failed clear rollback.
	mu.Lock()
	for i := range 25 {
		candyResults["7"] = appendRecent(candyResults["7"], last)
		pelicanResults["7"] = appendRecent(pelicanResults["7"], pelicanResult{Variant: pelicanVariants[i%2].ID})
	}
	err = saveStateLocked()
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedState
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &saved); err != nil || len(saved.Results["7"]) != 20 || len(saved.Pelicans["7"]) != 20 || saved.Pelicans["7"][0].Variant == saved.Pelicans["7"][1].Variant {
		t.Fatalf("history retention: %v", err)
	}
	oldPath := statePath
	statePath = filepath.Join(t.TempDir(), "missing", "history.json")
	_, status := Clear("candy")
	if status != 500 || len(candyResults["7"]) != 20 {
		t.Fatal("clear failure discarded history")
	}
	statePath = oldPath
	legacy := `{"results":{"7":[{"account_id":7,"group_id":1,"run_id":"old-candy","model":"preserved","ok":true,"answer":"21"}]},"pelicans":{"7":[{"id":"old-pelican","account_id":7,"group_id":1,"run_id":"old-run","model":"preserved","html":"<html>old</html>"}]},"fingerprints":{"7":[{"attribution":"retired"}]},"modeltraces":{"7":[{"id":"retired"}]}}`
	if err = os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	fingerprintResults = map[string][]fingerprintResult{}
	loadState()
	raw, err = os.ReadFile(statePath)
	saved = persistedState{}
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("cannot read migrated history")
	}
	if saved.SchemaVersion != 3 || len(saved.Fingerprints) != 0 || strings.Contains(string(raw), "retired") || saved.Results["7"][0].RunID != "old-candy" || saved.Results["7"][0].Status != "" || saved.Results["7"][0].Attempts != 0 || saved.Pelicans["7"][0].HTML != "<html>old</html>" || saved.Pelicans["7"][0].Variant != "original" {
		t.Fatalf("minimal migration changed retained records: %s", raw)
	}
	backup, err := os.ReadFile(filepath.Join(filepath.Dir(statePath), "history.v0.json"))
	if err != nil || string(backup) != legacy {
		t.Fatal("unversioned recovery copy changed original bytes")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(statePath), "history.v0.json")); err != nil {
		t.Fatal(err)
	}
	loaded = false
	candyResults = map[string][]candyResult{}
	pelicanResults = map[string][]pelicanResult{}
	loadState()
	if len(candyResults["7"]) != 1 || len(pelicanResults["7"]) != 1 {
		t.Fatal("migrated records did not reload")
	}
	saved.SchemaVersion = 1
	saved.Fingerprints = map[string][]fingerprintResult{"7": {fp}}
	saved.Pelicans["7"][0].Variant = ""
	v1, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	v1Backup := filepath.Join(filepath.Dir(statePath), "history.v1.json")
	if err := os.Mkdir(v1Backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, v1, 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	_, status = Clear("all")
	raw, _ = os.ReadFile(statePath)
	if stateLoadError == nil || storageError == "" || status != 500 || string(raw) != string(v1) || len(fingerprintResults["7"]) != 1 || pelicanResults["7"][0].Variant != "original" {
		t.Fatal("backup failure did not retain records and block writes")
	}
	if err := os.Remove(v1Backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	stateLoadError, loaded = nil, false
	loadState()
	raw, _ = os.ReadFile(statePath)
	backup, err = os.ReadFile(v1Backup)
	if err != nil || string(backup) != string(v1) || string(raw) != string(v1) || storageError == "" || len(fingerprintResults["7"]) != 1 {
		t.Fatal("failed migration save lost the original, copy, or memory records")
	}
	if err := os.Chmod(v1Backup, 0644); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	raw, _ = os.ReadFile(statePath)
	if err := json.Unmarshal(raw, &saved); err != nil || saved.SchemaVersion != 3 || saved.Pelicans["7"][0].Variant != "original" {
		t.Fatal("schema-1 migration failed")
	}
	wantFingerprint, _ := json.Marshal(fp)
	gotFingerprint, _ := json.Marshal(saved.Fingerprints["7"][0])
	if string(gotFingerprint) != string(wantFingerprint) || saved.Results["7"][0].RunID != "old-candy" || saved.Pelicans["7"][0].HTML != "<html>old</html>" {
		t.Fatal("schema-1 migration changed retained data")
	}
	if info, err := os.Stat(v1Backup); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recovery copy permissions")
	}
	if err := os.Remove(v1Backup); err != nil {
		t.Fatal(err)
	}
	// Test v2 -> v3 migration with history.v2.json backup.
	saved.SchemaVersion = 2
	saved.Pelicans["7"][0].Variant = "all-in-one"
	v2, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	v2Backup := filepath.Join(filepath.Dir(statePath), "history.v2.json")
	if err := os.WriteFile(statePath, v2, 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	raw, _ = os.ReadFile(statePath)
	backup, err = os.ReadFile(v2Backup)
	if err != nil || string(backup) != string(v2) {
		t.Fatal("v2 backup did not preserve exact v2 bytes")
	}
	if info, err := os.Stat(v2Backup); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("v2 backup permissions")
	}
	if err := json.Unmarshal(raw, &saved); err != nil || saved.SchemaVersion != 3 || saved.Pelicans["7"][0].Variant != "all-in-one" {
		t.Fatal("schema-2 to schema-3 migration failed")
	}
	if err := os.Remove(v2Backup); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	if _, err := os.Stat(v2Backup); !os.IsNotExist(err) {
		t.Fatal("schema-3 reload created a migration copy")
	}
	invalidMarker := `{"schema_version":null,"results":{"7":[]}}`
	if err = os.WriteFile(statePath, []byte(invalidMarker), 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	if len(candyResults["7"]) != 1 || storageError == "" {
		t.Fatal("invalid marker replaced loaded history")
	}
	_, status = Clear("all")
	raw, _ = os.ReadFile(statePath)
	if status != 500 || string(raw) != invalidMarker {
		t.Fatal("invalid marker was migrated or overwritten")
	}
	stateLoadError = nil
	future := `{"schema_version":4}`
	if err := os.WriteFile(statePath, []byte(future), 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	_, status = Clear("all")
	raw, _ = os.ReadFile(statePath)
	if status != 500 || string(raw) != future {
		t.Fatal("future schema was overwritten")
	}
	stateLoadError = nil
	if err = os.WriteFile(statePath, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded = false
	loadState()
	_, status = Clear("all")
	if status != 500 {
		t.Fatal("corrupt history was overwritten")
	}
	raw, _ = os.ReadFile(statePath)
	if string(raw) != "null" {
		t.Fatal("original corrupt file changed")
	}
}
func TestCodexEvalStreamSemantics(t *testing.T) {
	text := "event: usage\ndata: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"21\"}]}}\n\n" +
		"data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":3}}}"
	out, err := readResponseStream(strings.NewReader(text))
	if err != nil || out.Usage.InputTokens != 7 || out.Usage.OutputTokens != 3 || len(out.Output) != 1 {
		t.Fatalf("stream: %+v %v", out, err)
	}
	out, err = readResponseStream(strings.NewReader("data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":9}}}\n\ndata: {\"type\":\"error\",\"message\":\"actual failure\"}\n\n"))
	if err == nil || !strings.Contains(err.Error(), "actual failure") || out.Usage.InputTokens != 9 {
		t.Fatalf("failed usage: %+v %v", out, err)
	}
}
func TestSIWCClassifications(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		body       string
		wantStatus string
		wantCode   string
		wantError  string
	}{
		{
			name:       "restricted",
			httpStatus: 403,
			body:       `{"error":{"code":"subscription_sharing_user_not_eligible","message":"Your account is not eligible to use subscription sharing"}}`,
			wantStatus: "restricted",
			wantCode:   "subscription_sharing_user_not_eligible",
			wantError:  "Your account is not eligible to use subscription sharing",
		},
		{
			name:       "limited-by-code",
			httpStatus: 403,
			body:       `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"Usage limit exceeded"}}`,
			wantStatus: "limited",
			wantCode:   "subscription_sharing_usage_limit_exceeded",
			wantError:  "Usage limit exceeded",
		},
		{
			name:       "limited-by-429",
			httpStatus: 429,
			body:       `{"error":{"code":"rate_limit_exceeded","message":"Too many requests"}}`,
			wantStatus: "limited",
			wantCode:   "rate_limit_exceeded",
			wantError:  "Too many requests",
		},
		{
			name:       "auth-error-by-code",
			httpStatus: 403,
			body:       `{"error":{"code":"subscription_sharing_invalid_user","message":"Invalid user"}}`,
			wantStatus: "auth_error",
			wantCode:   "subscription_sharing_invalid_user",
			wantError:  "Invalid user",
		},
		{
			name:       "auth-error-by-401",
			httpStatus: 401,
			body:       `{"error":{"code":"invalid_api_key","message":"Unauthorized"}}`,
			wantStatus: "auth_error",
			wantCode:   "invalid_api_key",
			wantError:  "Unauthorized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Initialize(t.TempDir(), func(_ context.Context, _ int64, body []byte, headers http.Header) (*http.Response, error) {
				if headers.Get("User-Agent") != "cpa-siwc-check/"+pluginVersion {
					t.Fatalf("unexpected User-Agent: %s", headers.Get("User-Agent"))
				}
				return &http.Response{
					StatusCode: tc.httpStatus,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				}, nil
			}, nil)
			res := evaluateSIWC(credential{ID: "7", Target: Target{1, 7}, Name: "codex-oauth", ctx: context.Background()}, "gpt-5.4")
			if res.Status != tc.wantStatus || res.Code != tc.wantCode || res.Error != tc.wantError || res.HTTPStatus != tc.httpStatus {
				t.Fatalf("got %+v, want status=%s code=%s err=%s http=%d", res, tc.wantStatus, tc.wantCode, tc.wantError, tc.httpStatus)
			}
		})
	}
}
