// sub2api-package: backend/internal/codexeval
package codexeval

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexInspectionConfirmationAndBoundaries(t *testing.T) {
	oldDelay := retryDelay
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = oldDelay })
	mu.Lock()
	loaded = false
	stateLoadError = nil
	storageError = ""
	candyResults = map[string][]candyResult{}
	mu.Unlock()
	var lock sync.Mutex
	calls := map[int64]int{}
	updates := map[int64]int{}
	verdicts := map[int64]InspectionVerdict{}
	stableReady := make(chan struct{})
	accounts := []InspectionAccount{
		{Target: Target{AccountID: 1}, Name: "codex-wrong", Available: true, Priority: 10},
		{Target: Target{AccountID: 2}, Name: "codex-correct", Available: true, Priority: 10},
		{Target: Target{AccountID: 3}, Name: "codex-mixed", Available: true, Priority: 10},
		{Target: Target{AccountID: 4}, Name: "codex-disabled", Available: false},
		{Target: Target{AccountID: 5}, Name: "codex-free", Available: true, PlanType: "free"},
		{Target: Target{AccountID: 6}, Name: "outside-old-filter", Available: true, Verdict: InspectionVerdictDegraded},
		{Target: Target{AccountID: 7}, Name: "codex-recovery", Available: true, Priority: 99, Verdict: InspectionVerdictDegraded},
		{Target: Target{AccountID: 8}, Name: "codex-failed", Available: true, Priority: 10},
	}
	for n := range accounts {
		accounts[n].GroupIDs = []int64{101}
		accounts[n].Type = "oauth"
		accounts[n].Enabled = true
		accounts[n].AutoInspectionEnabled = true
		if accounts[n].PlanType == "" {
			accounts[n].PlanType = "pro"
		}
	}
	Initialize(t.TempDir(), func(ctx context.Context, id int64, _ []byte, _ http.Header) (*http.Response, error) {
		lock.Lock()
		calls[id]++
		n := calls[id]
		lock.Unlock()
		if id == 2 && n == 1 {
			select {
			case <-stableReady:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				t.Error("ready stable account waited for an unrelated detection")
			}
		}
		if id == 7 && n == 2 {
			lock.Lock()
			if updates[1] != 99 {
				t.Error("confirmed degradation waited for unrelated recovery confirmation")
			}
			lock.Unlock()
		}
		if id == 8 {
			return nil, fmt.Errorf("fake upstream failed")
		}
		if id == 1 && n == 1 {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("temporary"))}, nil
		}
		answer := "20"
		if id == 2 || id == 7 || (id == 3 && n == 2) {
			answer = "21"
		}
		body := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", answer)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}, func(context.Context, int64) (string, error) { return "fake", nil })
	notified := 0
	hooks := InspectionHooks{
		List: func(_ context.Context, s InspectionSettings) ([]InspectionAccount, error) {
			if s.GroupID != 101 {
				t.Errorf("wrong selected group: %d", s.GroupID)
			}
			return accounts, nil
		},
		Get: func(_ context.Context, id int64) (InspectionAccount, error) {
			lock.Lock()
			defer lock.Unlock()
			for _, a := range accounts {
				if a.AccountID == id {
					return a, nil
				}
			}
			panic("unexpected account")
		},
		PersistVerdict: func(_ context.Context, id int64, verdict InspectionVerdict, _ InspectionSettings) error {
			lock.Lock()
			defer lock.Unlock()
			for n := range accounts {
				if accounts[n].AccountID == id {
					current := accounts[n].Verdict
					if current == "" {
						current = InspectionVerdictNormal
					}
					if current == verdict {
						return nil
					}
					verdicts[id] = verdict
					accounts[n].Verdict = verdict
				}
			}
			return nil
		},
		UpdatePriority: func(_ context.Context, id int64, priority int) error {
			lock.Lock()
			defer lock.Unlock()
			for n := range accounts {
				if accounts[n].AccountID != id {
					continue
				}
				want := InspectionVerdictDegraded
				if priority == 10 {
					want = InspectionVerdictNormal
				}
				current := accounts[n].Verdict
				if current == "" {
					current = InspectionVerdictNormal
				}
				if current != want {
					t.Error("priority write preceded verdict save")
				}
				accounts[n].Priority = priority
			}
			updates[id] = priority
			if id == 6 {
				close(stableReady)
			}
			return nil
		},
		Notify: func(context.Context, InspectionSettings, string, string) error {
			notified++
			return fmt.Errorf("notification failed")
		},
	}
	i := newInspector(hooks)
	defer i.cancel()
	i.poll = time.Millisecond
	i.recovery = time.Millisecond
	s := DefaultInspectionSettings()
	s.GroupID, s.AccountPlan = 101, "Pro ALL"
	s.AdjustPriority = true
	i.configure(s)
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	if calls[1] != 4 || calls[2] != 1 || calls[3] != 3 || calls[7] != 3 || calls[8] != 1 || calls[4] != 0 || calls[5] != 0 || calls[6] != 1 {
		t.Fatalf("wrong test counts: %v", calls)
	}
	if len(updates) != 3 || updates[1] != 99 || updates[6] != 99 || updates[7] != 10 {
		t.Fatalf("wrong priority updates: %v", updates)
	}
	if len(verdicts) != 2 || verdicts[1] != InspectionVerdictDegraded || verdicts[7] != InspectionVerdictNormal {
		t.Fatalf("wrong verdict saves: %v", verdicts)
	}
	if notified != 2 {
		t.Fatalf("notifications=%d", notified)
	}
	state := i.snapshot()
	if state.Running || state.Done != 6 {
		t.Fatalf("unexpected state: %+v", state)
	}
	for _, r := range state.Results {
		if verdicts[r.AccountID] != "" && r.NotificationError == "" {
			t.Fatal("missing notification failure")
		}
		if verdicts[r.AccountID] == "" && r.NotificationError != "" {
			t.Fatal("stable state sent a notification")
		}
	}

	// Verdict transitions notify even when both other actions are off.
	clear(updates)
	clear(verdicts)
	notified = 0
	s.AdjustPriority = false
	i.configure(s)
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	if len(updates) != 0 || notified != 1 {
		t.Fatal("adjustment-off transition lost its notification or wrote priority")
	}
	if len(verdicts) != 1 || verdicts[3] != InspectionVerdictDegraded {
		t.Fatalf("adjustment off lost verdict transition: %v", verdicts)
	}
}

func TestCodexInspectionInvalidInitialSettingsBlockStart(t *testing.T) {
	settings := DefaultInspectionSettings()
	settings.Enabled = true
	settings.DegradedPriority = 5
	i := newInspector(InspectionHooks{
		Load: func(context.Context) (InspectionSettings, error) { return settings, nil },
	})
	defer i.cancel()

	i.loadInitialSettings()
	state := i.snapshot()
	if state.Error == "" || state.NextRunAt != nil || i.settingsLoaded {
		t.Fatalf("invalid settings were configured or scheduled: %+v", state)
	}
	if err := i.start(); err == nil {
		t.Fatal("manual inspection started with invalid loaded settings")
	}
}

func TestCodexInspectionCancelAndSchedule(t *testing.T) {
	mu.Lock()
	loaded = false
	stateLoadError = nil
	storageError = ""
	candyResults = map[string][]candyResult{}
	mu.Unlock()
	started := make(chan struct{}, 1)
	Initialize(t.TempDir(), func(ctx context.Context, _ int64, _ []byte, _ http.Header) (*http.Response, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(context.Context, int64) (string, error) { return "fake", nil })
	a := InspectionAccount{Target: Target{AccountID: 42}, GroupIDs: []int64{101}, Type: "oauth", Available: true, Enabled: true, AutoInspectionEnabled: true}
	i := newInspector(InspectionHooks{List: func(context.Context, InspectionSettings) ([]InspectionAccount, error) {
		return []InspectionAccount{a}, nil
	}, Get: func(context.Context, int64) (InspectionAccount, error) { return a, nil },
		PersistVerdict: func(context.Context, int64, InspectionVerdict, InspectionSettings) error {
			t.Error("cancelled run saved verdict")
			return nil
		},
		UpdatePriority: func(context.Context, int64, int) error { t.Error("cancelled run wrote account"); return nil }})
	defer i.cancel()
	i.poll = time.Millisecond
	s := DefaultInspectionSettings()
	s.Enabled = true
	s.GroupID = 101
	s.AdjustPriority = true
	i.configure(s)
	before := i.snapshot().NextRunAt
	if before == nil || time.Until(*before) < 15*time.Minute-time.Second || time.Until(*before) > 35*time.Minute {
		t.Fatal("schedule outside bounds")
	}
	if i.snapshot().NextRunAt != before {
		t.Fatal("state read reset schedule")
	}
	s.NtfyTopic = "changed"
	i.configure(s)
	if i.snapshot().NextRunAt != before {
		t.Fatal("autosave reset the pending schedule")
	}
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := i.start(); err == nil {
		t.Fatal("overlapping run accepted")
	}
	s.Enabled = false
	i.configure(s)
	if !i.snapshot().Running {
		t.Fatal("disable cancelled active round")
	}
	i.stop()
	<-i.runDone
	state := i.snapshot()
	if state.Running || state.Phase != "cancelled" || state.NextRunAt != nil {
		t.Fatalf("wrong stop state: %+v", state)
	}
	s.MinWaitMinutes = 17
	s.MaxWaitMinutes = 17
	if inspectionDelay(s) != 17*time.Minute {
		t.Fatal("equal bounds not fixed")
	}
}

func TestCodexInspectionNotifications(t *testing.T) {
	var ntfy, bark bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/topic" {
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture" || password != "secret" {
				t.Error("missing ntfy auth")
			}
			ntfy = true
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/private-bark-key" {
			bark = true
			_ = r.ParseForm()
			if r.Form.Get("body") != "message" {
				t.Error("missing Bark body")
			}
		}
	}))
	defer server.Close()
	s := DefaultInspectionSettings()
	s.NtfyServer = server.URL
	s.NtfyTopic = "topic"
	s.NtfyUsername = "fixture"
	s.NtfyPassword = "secret"
	s.BarkURL = server.URL + "/private-bark-key"
	err := SendInspectionNotification(context.Background(), s, "title", "message")
	if err == nil || !strings.Contains(err.Error(), "ntfy: HTTP 503") || strings.Contains(err.Error(), "private-bark-key") || !ntfy || !bark {
		t.Fatalf("unexpected notification result: %v", err)
	}
	public := s.Public()
	if public.NtfyPassword != "" || public.BarkURL != "" || !public.NtfyPasswordConfigured || !public.BarkURLConfigured {
		t.Fatal("secret exposed")
	}
}

func TestCodexInspectionRestartSchedulesFreshWait(t *testing.T) {
	s := DefaultInspectionSettings()
	s.Enabled = true
	s.GroupID = 101
	loaded := make(chan struct{})
	i := newInspector(InspectionHooks{Load: func(context.Context) (InspectionSettings, error) { close(loaded); return s, nil }})
	i.loadInitialSettings()
	go i.loop()
	defer func() { i.cancel(); <-i.done }()
	<-loaded
	state := i.snapshot()
	if state.NextRunAt == nil || state.Running || state.StartedAt != nil || len(state.Results) != 0 {
		t.Fatalf("restart resumed work or missed schedule: %+v", state)
	}
	first := *state.NextRunAt
	s.MinWaitMinutes = 16
	i.configure(s)
	if next := i.snapshot().NextRunAt; next == nil || next.Equal(first) {
		t.Fatal("configuration did not draw a fresh wait")
	}
	s.Enabled = false
	i.configure(s)
	if err := i.startRun(true); err != nil || i.snapshot().Running {
		t.Fatal("disabled stale timer started a round")
	}
}

func TestCodexInspectionCandySelection(t *testing.T) {
	s := DefaultInspectionSettings()
	s.GroupID = 202
	a := InspectionAccount{Target: Target{AccountID: 33}, GroupIDs: []int64{101, 202}, Type: "oauth", PlanType: "pro_lite", Available: true, Enabled: true, AutoInspectionEnabled: true}
	if !inspectionEligible(a, s) {
		t.Fatal("shared group member was excluded")
	}
	s.AccountPlan = "Pro ALL"
	if !inspectionEligible(a, s) {
		t.Fatal("Pro ALL lost a Pro alias")
	}
	s.AccountPlan = "Pro 100"
	if !inspectionEligible(a, s) {
		t.Fatal("exact plan lost a Pro alias")
	}
	s.AccountPlan = "Free"
	if inspectionEligible(a, s) {
		t.Fatal("a different plan matched")
	}
	a.PlanType = "free"
	if !inspectionEligible(a, s) {
		t.Fatal("the Candy Free filter was overridden")
	}
	a.Type = "apikey"
	if inspectionEligible(a, s) {
		t.Fatal("API keys matched an OAuth plan")
	}
	s.AccountPlan = "all"
	s.AccountType = "oauth"
	if inspectionEligible(a, s) {
		t.Fatal("account type filter was ignored")
	}
	s.AccountType = "all"
	a.GroupIDs = []int64{101}
	if inspectionEligible(a, s) {
		t.Fatal("an account outside the saved group matched")
	}
	a.GroupIDs = []int64{202}
	a.Available = false
	if inspectionEligible(a, s) {
		t.Fatal("an unavailable account matched")
	}
	a.Available = true
	a.Platform = "anthropic"
	a.Type = "apikey"
	a.Name = "claude-prod-1"
	a.Email = "ops@example.com"
	s.AccountType = "anthropic:apikey"
	s.IncludePatterns = []string{"claude-*"}
	s.ExcludePatterns = []string{"*-backup"}
	if !inspectionEligible(a, s) {
		t.Fatal("anthropic:apikey matching include pattern was excluded")
	}
	s.IncludePatterns = []string{"*@example.com"}
	if !inspectionEligible(a, s) {
		t.Fatal("email matching include pattern was excluded")
	}
	s.ExcludePatterns = []string{"*@example.com"}
	if inspectionEligible(a, s) {
		t.Fatal("email matching exclude pattern was included")
	}
	a.Platform = "deepseek"
	s.AccountType = "deepseek:apikey"
	s.IncludePatterns = nil
	s.ExcludePatterns = nil
	if !inspectionEligible(a, s) {
		t.Fatal("deepseek:apikey account was excluded")
	}
	if (InspectionSettings{GroupID: 202, AccountType: "deepseek:apikey", AccountPlan: "all", Model: "deepseek-chat", MaxTestsPerAccount: 3, MinWaitMinutes: 15, MaxWaitMinutes: 35, NormalPriority: 10, DegradedPriority: 99}).Validate() != nil {
		t.Fatal("deepseek:apikey account_type failed validation")
	}
}

func TestCodexInspectionFingerprintModeAndWildcards(t *testing.T) {
	oldDelay := retryDelay
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = oldDelay })
	mu.Lock()
	loaded = false
	stateLoadError = nil
	storageError = ""
	candyResults = map[string][]candyResult{}
	fingerprintResults = map[string][]fingerprintResult{}
	mu.Unlock()

	accounts := []InspectionAccount{
		{Target: Target{AccountID: 1}, Name: "codex-match", Email: "match@team.io", Platform: "openai", Type: "oauth", PlanType: "pro", GroupIDs: []int64{101}, Available: true, Enabled: true, AutoInspectionEnabled: true, Priority: 10},
		{Target: Target{AccountID: 2}, Name: "codex-excluded", Email: "skip@team.io", Platform: "openai", Type: "oauth", PlanType: "pro", GroupIDs: []int64{101}, Available: true, Enabled: true, AutoInspectionEnabled: true, Priority: 10},
	}
	var lock sync.Mutex
	calls := 0
	verdicts := map[int64]InspectionVerdict{}
	Initialize(t.TempDir(), func(_ context.Context, _ int64, _ []byte, _ http.Header) (*http.Response, error) {
		lock.Lock()
		calls++
		lock.Unlock()
		nums := make([]string, 320)
		for idx := range nums {
			nums[idx] = fmt.Sprintf("%d", (idx*37+13)%355+1)
		}
		answer := strings.Join(nums, ",")
		body := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", answer)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}, func(context.Context, int64) (string, error) { return "fp-account", nil })

	i := newInspector(InspectionHooks{
		List: func(context.Context, InspectionSettings) ([]InspectionAccount, error) {
			return accounts, nil
		},
		Get: func(_ context.Context, id int64) (InspectionAccount, error) {
			for _, acc := range accounts {
				if acc.AccountID == id {
					return acc, nil
				}
			}
			return InspectionAccount{}, fmt.Errorf("missing %d", id)
		},
		PersistVerdict: func(_ context.Context, id int64, v InspectionVerdict, _ InspectionSettings) error {
			verdicts[id] = v
			for idx := range accounts {
				if accounts[idx].AccountID == id {
					accounts[idx].Verdict = v
				}
			}
			return nil
		},
	})
	defer i.cancel()
	i.poll = time.Millisecond
	i.recovery = time.Hour

	s := DefaultInspectionSettings()
	s.GroupID = 101
	s.TestType = "fingerprint"
	s.MaxTestsPerAccount = 3
	s.Model = "unmatched-custom-model-for-degradation"
	s.IncludePatterns = []string{"*@team.io"}
	s.ExcludePatterns = []string{"codex-excluded"}
	i.configure(s)
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	state := i.snapshot()
	if len(state.Results) != 1 || state.Results[0].AccountID != 1 {
		t.Fatalf("wildcard filter did not select only account 1: %+v", state.Results)
	}
	r := state.Results[0]
	if r.Tests != 1 || len(r.RunIDs) != 1 || calls != 3 || r.Outcome != "recommended" || r.TargetPriority == nil || *r.TargetPriority != 99 {
		t.Fatalf("fingerprint transition did not apply in a single run without confirmation rounds: calls=%d result=%+v", calls, r)
	}
	if verdicts[1] != InspectionVerdictDegraded {
		t.Fatalf("expected fingerprint mismatch to mark account degraded: %+v", verdicts)
	}

	mu.Lock()
	predicted := fingerprintResults["1"][0].Attribution.Prediction
	mu.Unlock()
	s.Model = predicted
	i.configure(s)
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	r = i.snapshot().Results[0]
	if r.Tests != 1 || len(r.RunIDs) != 1 || calls != 6 || r.Outcome != "recommended" || r.TargetPriority == nil || *r.TargetPriority != 10 || verdicts[1] != InspectionVerdictNormal {
		t.Fatalf("fingerprint recovery did not apply in a single run without recovery_wait: calls=%d result=%+v verdicts=%+v", calls, r, verdicts)
	}
}

func TestCodexInspectionDegradedWrongOnce(t *testing.T) {
	mu.Lock()
	loaded, stateLoadError, storageError = false, nil, ""
	candyResults = map[string][]candyResult{}
	mu.Unlock()
	calls, saves, applications, priorityWrites, notifications := 0, 0, 0, 0, 0
	answer := "20"
	Initialize(t.TempDir(), func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
		calls++
		body := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", answer)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}, func(context.Context, int64) (string, error) { return "verdict lifecycle", nil })
	listed := InspectionAccount{Target: Target{AccountID: 91}, GroupIDs: []int64{101}, Type: "oauth", Available: true, Enabled: true, AutoInspectionEnabled: true, Priority: 10}
	current := listed
	i := newInspector(InspectionHooks{
		List: func(context.Context, InspectionSettings) ([]InspectionAccount, error) {
			return []InspectionAccount{listed}, nil
		},
		Get: func(context.Context, int64) (InspectionAccount, error) { return current, nil },
		PersistVerdict: func(_ context.Context, _ int64, verdict InspectionVerdict, settings InspectionSettings) error {
			if settings.ControlModels || settings.DegradedRemoveModels[0] != "gpt-6.1-sol" {
				t.Error("transition did not receive round model settings")
			}
			applications++
			if current.Verdict == verdict || current.Verdict == "" && verdict == InspectionVerdictNormal {
				return nil
			}
			saves++
			current.Verdict = verdict
			return nil
		},
		UpdatePriority: func(_ context.Context, _ int64, priority int) error {
			priorityWrites++
			current.Priority = priority
			return nil
		},
		Notify: func(_ context.Context, _ InspectionSettings, _ string, message string) error {
			notifications++
			if strings.Contains(message, "priority") || !strings.Contains(message, "→") {
				t.Error("notification did not describe the verdict transition")
			}
			return nil
		},
	})
	defer i.cancel()
	i.poll, i.recovery = time.Millisecond, time.Millisecond
	s := DefaultInspectionSettings()
	s.GroupID = 101
	i.configure(s)
	for _, step := range []struct {
		name    string
		answer  string
		tests   int
		verdict InspectionVerdict
		saves   int
		notify  bool
		sent    int
	}{
		{"degradation", "20", 3, InspectionVerdictDegraded, 1, true, 1},
		{"stable wrong", "20", 1, InspectionVerdictDegraded, 1, true, 1},
		{"recovery", "21", 3, InspectionVerdictNormal, 2, true, 2},
		{"stable correct", "21", 1, InspectionVerdictNormal, 2, true, 2},
		{"muted degradation", "20", 3, InspectionVerdictDegraded, 3, false, 2},
	} {
		answer = step.answer
		s.NotifyEnabled = step.notify
		i.configure(s)
		beforeCalls, beforeResults := calls, len(candyResults["91"])
		if err := i.start(); err != nil {
			t.Fatal(err)
		}
		<-i.runDone
		r := i.snapshot().Results[0]
		if calls-beforeCalls != step.tests || r.Tests != step.tests || len(candyResults["91"])-beforeResults != step.tests || current.Verdict != step.verdict || saves != step.saves || notifications != step.sent || current.Priority != 10 || r.Error != "" {
			t.Fatalf("%s: calls=%d result=%+v account=%+v saves=%d", step.name, calls-beforeCalls, r, current, saves)
		}
		if step.tests == 1 && (r.Outcome != "unchanged" || r.TargetPriority != nil) {
			t.Fatalf("stable verdict changed: %+v", r)
		}
	}
	if applications != 5 || priorityWrites != 0 || notifications != 2 {
		t.Fatalf("stable states were skipped or adjustment-off changed priority: applications=%d writes=%d notifications=%d", applications, priorityWrites, notifications)
	}
	s.AdjustPriority, s.DegradedPriority, s.NotifyEnabled = true, 80, true
	i.configure(s)
	for _, wantWrites := range []int{1, 1} {
		beforeCalls := calls
		if err := i.start(); err != nil {
			t.Fatal(err)
		}
		<-i.runDone
		if calls-beforeCalls != 1 || current.Priority != 80 || priorityWrites != wantWrites || saves != 3 || notifications != 2 {
			t.Fatalf("stable degraded state did not repair priority silently: calls=%d account=%+v writes=%d notifications=%d", calls-beforeCalls, current, priorityWrites, notifications)
		}
	}
}

func TestCodexInspectionWriteFailures(t *testing.T) {
	for _, failedWrite := range []string{"verdict", "priority", "matching"} {
		t.Run(failedWrite, func(t *testing.T) {
			mu.Lock()
			loaded, stateLoadError, storageError = false, nil, ""
			candyResults = map[string][]candyResult{}
			mu.Unlock()
			Initialize(t.TempDir(), func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"20\"}]}]}}\n\n"))}, nil
			}, func(context.Context, int64) (string, error) { return "write failure", nil })
			a := InspectionAccount{Target: Target{AccountID: 93}, GroupIDs: []int64{101}, Type: "oauth", Available: true, Enabled: true, AutoInspectionEnabled: true, Priority: 42, Verdict: InspectionVerdictNormal}
			if failedWrite == "matching" {
				a.Priority = 99
			}
			originalPriority := a.Priority
			verdictWrites, priorityWrites, notifications := 0, 0, 0
			i := newInspector(InspectionHooks{
				List: func(context.Context, InspectionSettings) ([]InspectionAccount, error) {
					return []InspectionAccount{a}, nil
				},
				Get: func(context.Context, int64) (InspectionAccount, error) { return a, nil },
				PersistVerdict: func(_ context.Context, _ int64, verdict InspectionVerdict, _ InspectionSettings) error {
					verdictWrites++
					if failedWrite == "verdict" {
						return fmt.Errorf("fixture verdict write failed")
					}
					a.Verdict = verdict
					return nil
				},
				UpdatePriority: func(_ context.Context, _ int64, priority int) error {
					priorityWrites++
					if a.Verdict != InspectionVerdictDegraded || priority != 99 {
						t.Error("priority write preceded verdict save or lost target")
					}
					return fmt.Errorf("fixture priority write failed")
				},
				Notify: func(context.Context, InspectionSettings, string, string) error {
					notifications++
					return nil
				},
			})
			defer i.cancel()
			i.poll = time.Millisecond
			s := DefaultInspectionSettings()
			s.GroupID, s.AdjustPriority, s.MaxTestsPerAccount = 101, true, 1
			i.configure(s)
			if err := i.start(); err != nil {
				t.Fatal(err)
			}
			<-i.runDone
			r := i.snapshot().Results[0]
			wantVerdict, wantPriorityWrites, wantNotifications := InspectionVerdictNormal, 0, 0
			wantOutcome, wantError := "error", "fixture "+failedWrite+" write failed"
			if failedWrite == "priority" {
				wantVerdict, wantPriorityWrites, wantNotifications = InspectionVerdictDegraded, 1, 1
			} else if failedWrite == "matching" {
				wantVerdict, wantNotifications = InspectionVerdictDegraded, 1
				wantOutcome, wantError = "recommended", ""
			}
			if verdictWrites != 1 || priorityWrites != wantPriorityWrites || notifications != wantNotifications || a.Verdict != wantVerdict || a.Priority != originalPriority || r.Outcome != wantOutcome || r.Error != wantError {
				t.Fatalf("writes=%d/%d account=%+v result=%+v", verdictWrites, priorityWrites, a, r)
			}
		})
	}
}

func TestCodexInspectionParticipationStopsLaterSends(t *testing.T) {
	mu.Lock()
	loaded, stateLoadError, storageError = false, nil, ""
	candyResults = map[string][]candyResult{}
	mu.Unlock()
	var lock sync.Mutex
	a := InspectionAccount{Target: Target{GroupID: 101, AccountID: 92}, GroupIDs: []int64{101}, Type: "oauth", Available: true, Enabled: true, Priority: 10}
	calls := 0
	confirmation := make(chan struct{})
	release := make(chan struct{})
	Initialize(t.TempDir(), func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
		calls++
		if calls == 2 {
			close(confirmation)
			<-release
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"20\"}]}]}}\n\n"))}, nil
	}, func(context.Context, int64) (string, error) { return "participation", nil })
	i := newInspector(InspectionHooks{
		List: func(context.Context, InspectionSettings) ([]InspectionAccount, error) {
			lock.Lock()
			defer lock.Unlock()
			return []InspectionAccount{a}, nil
		},
		Get: func(context.Context, int64) (InspectionAccount, error) {
			lock.Lock()
			defer lock.Unlock()
			return a, nil
		},
		PersistVerdict: func(context.Context, int64, InspectionVerdict, InspectionSettings) error {
			t.Error("opted-out account verdict changed")
			return nil
		},
		UpdatePriority: func(context.Context, int64, int) error { t.Error("opted-out account priority changed"); return nil },
		Notify: func(context.Context, InspectionSettings, string, string) error {
			t.Error("opted-out account notified")
			return nil
		},
	})
	defer i.cancel()
	i.poll = time.Millisecond
	s := DefaultInspectionSettings()
	s.GroupID, s.AdjustPriority = 101, true
	i.configure(s)
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	if calls != 0 || len(i.snapshot().Results) != 0 {
		t.Fatal("opted-out account selected")
	}
	lock.Lock()
	a.AutoInspectionEnabled = true
	lock.Unlock()
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-confirmation:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation did not start")
	}
	lock.Lock()
	a.AutoInspectionEnabled = false
	lock.Unlock()
	close(release)
	<-i.runDone
	r := i.snapshot().Results[0]
	if calls != 2 || r.Tests != 2 || r.Outcome != "unchanged" || r.Error != "" || r.TargetPriority != nil || len(candyResults["92"]) != 2 {
		t.Fatalf("opt-out calls=%d result=%+v", calls, r)
	}
	for _, result := range candyResults["92"] {
		if result.Error != "" {
			t.Fatal("opt-out created a test error")
		}
	}
	// Manual fixed-count tests remain usable for this opted-out account.
	_, err := Start(context.Background(), StartRequest{Type: "candy", Targets: []Target{a.Target}, Model: "fixture", Runs: 3})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	if calls != 5 || len(candyResults["92"]) != 5 {
		t.Fatalf("manual count changed: %d", calls)
	}
	// Opt-out during backoff stops this logical result before another send.
	oldDelay := retryDelay
	retryDelay = 20 * time.Millisecond
	t.Cleanup(func() { retryDelay = oldDelay })
	lock.Lock()
	a.AutoInspectionEnabled = true
	lock.Unlock()
	sender = func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
		calls++
		lock.Lock()
		a.AutoInspectionEnabled = false
		lock.Unlock()
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("temporary"))}, nil
	}
	if err := i.start(); err != nil {
		t.Fatal(err)
	}
	<-i.runDone
	r = i.snapshot().Results[0]
	if calls != 6 || r.Tests != 0 || r.Outcome != "unchanged" || r.Error != "" || len(candyResults["92"]) != 5 {
		t.Fatalf("backoff opt-out calls=%d result=%+v history=%d", calls, r, len(candyResults["92"]))
	}
}
