package codexeval

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type InspectionVerdict string

const (
	InspectionVerdictNormal   InspectionVerdict = "normal"
	InspectionVerdictDegraded InspectionVerdict = "degraded"
)

type InspectionAccount struct {
	Target
	Name                  string
	Email                 string `json:"email,omitempty"`
	Platform              string `json:"platform,omitempty"`
	Priority              int
	Verdict               InspectionVerdict
	Available             bool
	GroupIDs              []int64
	Type                  string
	PlanType              string
	Enabled               bool
	AutoInspectionEnabled bool
}

type InspectionHooks struct {
	Load           func(context.Context) (InspectionSettings, error)
	List           func(context.Context, InspectionSettings) ([]InspectionAccount, error)
	Get            func(context.Context, int64) (InspectionAccount, error)
	PersistVerdict func(context.Context, int64, InspectionVerdict, InspectionSettings) error
	UpdatePriority func(context.Context, int64, int) error
	Notify         func(context.Context, InspectionSettings, string, string) error
}

type InspectionResult struct {
	AccountID         int64    `json:"account_id"`
	AccountName       string   `json:"account_name"`
	Priority          int      `json:"priority"`
	TargetPriority    *int     `json:"target_priority,omitempty"`
	RunIDs            []string `json:"run_ids"`
	Tests             int      `json:"tests"`
	Outcome           string   `json:"outcome"`
	Error             string   `json:"error,omitempty"`
	NotificationError string   `json:"notification_error,omitempty"`
}

type InspectionState struct {
	Enabled    bool               `json:"enabled"`
	Running    bool               `json:"running"`
	Cancelling bool               `json:"cancelling"`
	Phase      string             `json:"phase"`
	NextRunAt  *time.Time         `json:"next_run_at"`
	StartedAt  *time.Time         `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at"`
	Total      int                `json:"total"`
	Done       int                `json:"done"`
	Error      string             `json:"error"`
	Results    []InspectionResult `json:"results"`
}

type inspector struct {
	engine         *Engine
	mu             sync.Mutex
	settings       InspectionSettings
	settingsLoaded bool
	hooks          InspectionHooks
	state          InspectionState
	ctx            context.Context
	cancel         context.CancelFunc
	runCancel      context.CancelFunc
	wake           chan struct{}
	done           chan struct{}
	runDone        chan struct{}
	poll           time.Duration
	recovery       time.Duration
	phaseLimit     time.Duration
}

func (engine *Engine) InitializeInspection(hooks InspectionHooks) {
	engine.mu.Lock()
	if engine.inspection != nil || engine.stopped {
		engine.mu.Unlock()
		return
	}
	i := engine.newInspector(hooks)
	engine.inspection = i
	engine.mu.Unlock()
	i.loadInitialSettings()
	go i.loop()
}

func (engine *Engine) newInspector(hooks InspectionHooks) *inspector {
	ctx, cancel := context.WithCancel(context.Background())
	i := &inspector{engine: engine, hooks: hooks, settings: DefaultInspectionSettings(), ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}), poll: 15 * time.Second, recovery: 240 * time.Second, phaseLimit: 30 * time.Minute}
	i.state = InspectionState{Phase: "idle", Results: []InspectionResult{}}
	return i
}

func (engine *Engine) StopInspection() {
	i := engine.getInspector()
	if i == nil {
		return
	}
	i.cancel()
	<-i.done
}

func (engine *Engine) getInspector() *inspector {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.inspection
}

func (engine *Engine) UpdateInspectionSettings(s InspectionSettings) {
	if i := engine.getInspector(); i != nil {
		i.configure(s)
	}
}
func (engine *Engine) CurrentInspectionState() InspectionState {
	i := engine.getInspector()
	if i == nil {
		return InspectionState{Phase: "idle", Results: []InspectionResult{}}
	}
	return i.snapshot()
}
func (engine *Engine) StartInspection() error {
	i := engine.getInspector()
	if i == nil {
		return fmt.Errorf("巡检尚未初始化")
	}
	return i.start()
}
func (engine *Engine) CancelInspection() {
	if i := engine.getInspector(); i != nil {
		i.stop()
	}
}

func (i *inspector) signal() {
	select {
	case i.wake <- struct{}{}:
	default:
	}
}
func (i *inspector) loadInitialSettings() {
	if i.hooks.Load == nil {
		i.configure(DefaultInspectionSettings())
		return
	}
	s, err := i.hooks.Load(i.ctx)
	if err != nil {
		i.mu.Lock()
		i.state.Error = err.Error()
		i.mu.Unlock()
		return
	}
	if err := s.Validate(); err != nil {
		i.mu.Lock()
		i.state.Error = err.Error()
		i.mu.Unlock()
		return
	}
	i.configure(s)
}
func (i *inspector) configure(s InspectionSettings) {
	i.mu.Lock()
	reschedule := !i.settingsLoaded || s.Enabled != i.settings.Enabled || s.MinWaitMinutes != i.settings.MinWaitMinutes || s.MaxWaitMinutes != i.settings.MaxWaitMinutes
	if !i.settingsLoaded {
		i.state.Error = ""
	}
	i.settings = s
	i.state.Enabled = s.Enabled
	i.settingsLoaded = true
	if reschedule {
		i.scheduleLocked()
	}
	i.mu.Unlock()
	i.signal()
}

func inspectionDelay(s InspectionSettings) time.Duration {
	minDelay, maxDelay := time.Duration(s.MinWaitMinutes)*time.Minute, time.Duration(s.MaxWaitMinutes)*time.Minute
	if maxDelay <= minDelay {
		return minDelay
	}
	return minDelay + time.Duration(rand.Int64N(int64(maxDelay-minDelay)+1))
}
func (i *inspector) scheduleLocked() {
	i.state.NextRunAt = nil
	if i.settings.Enabled && !i.state.Running && i.ctx.Err() == nil {
		next := time.Now().Add(inspectionDelay(i.settings))
		i.state.NextRunAt = &next
	}
}

func (i *inspector) loop() {
	defer close(i.done)
	for {
		i.mu.Lock()
		next := i.state.NextRunAt
		i.mu.Unlock()
		var timer *time.Timer
		var tick <-chan time.Time
		if next != nil {
			timer = time.NewTimer(max(time.Until(*next), 0))
			tick = timer.C
		}
		select {
		case <-i.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			i.mu.Lock()
			done := i.runDone
			i.mu.Unlock()
			if done != nil {
				<-done
			}
			return
		case <-i.wake:
			if timer != nil {
				timer.Stop()
			}
		case <-tick:
			_ = i.startRun(true)
		}
	}
}

func (i *inspector) snapshot() InspectionState {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := i.state
	s.Results = append([]InspectionResult{}, s.Results...)
	for n := range s.Results {
		s.Results[n].RunIDs = append([]string{}, s.Results[n].RunIDs...)
	}
	return s
}

func (i *inspector) start() error {
	return i.startRun(false)
}

func inspectionTestType(s InspectionSettings) string {
	if strings.TrimSpace(s.TestType) == "fingerprint" {
		return "fingerprint"
	}
	return "candy"
}

func (i *inspector) startRun(automatic bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if automatic && (!i.settings.Enabled || i.state.NextRunAt == nil || time.Until(*i.state.NextRunAt) > 0) {
		return nil
	}
	if !i.settingsLoaded {
		return fmt.Errorf("巡检设置尚未加载")
	}
	if i.ctx.Err() != nil {
		return fmt.Errorf("应用正在停止")
	}
	if i.state.Running {
		return fmt.Errorf("已有巡检正在执行")
	}
	if err := i.settings.Validate(); err != nil {
		return err
	}
	if i.settings.GroupID == 0 {
		return fmt.Errorf("请选择糖果测试分组")
	}
	ctx, cancel := context.WithCancel(i.ctx)
	i.runCancel = cancel
	i.runDone = make(chan struct{})
	now := time.Now().UTC()
	i.state = InspectionState{Enabled: i.settings.Enabled, Running: true, Phase: "testing", StartedAt: &now, Results: []InspectionResult{}}
	go i.run(ctx, i.settings, i.runDone)
	i.signal()
	return nil
}
func (i *inspector) stop() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runCancel != nil {
		i.state.Cancelling = true
		i.runCancel()
	}
}
func (i *inspector) phase(phase string) { i.mu.Lock(); i.state.Phase = phase; i.mu.Unlock() }
func (i *inspector) result(index int, update func(*InspectionResult)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	update(&i.state.Results[index])
}

func matchesInspectionAccountType(a InspectionAccount, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" || filter == "all" {
		return true
	}
	if platform, credType, ok := strings.Cut(filter, ":"); ok {
		acctPlatform := strings.TrimSpace(a.Platform)
		if acctPlatform == "" {
			acctPlatform = "openai"
		}
		return strings.EqualFold(acctPlatform, platform) && strings.EqualFold(a.Type, credType)
	}
	return strings.EqualFold(a.Type, filter)
}

func matchesAnyInspectionPattern(a InspectionAccount, patterns []string) bool {
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		for _, value := range []string{a.Email, a.Name} {
			if value == "" {
				continue
			}
			if matched, err := path.Match(pattern, value); err == nil && matched {
				return true
			}
		}
	}
	return false
}

func matchesInspectionPatterns(a InspectionAccount, include, exclude []string) bool {
	hasInclude := false
	for _, raw := range include {
		if strings.TrimSpace(raw) != "" {
			hasInclude = true
			break
		}
	}
	if hasInclude && !matchesAnyInspectionPattern(a, include) {
		return false
	}
	if matchesAnyInspectionPattern(a, exclude) {
		return false
	}
	return true
}

func inspectionEligible(a InspectionAccount, s InspectionSettings) bool {
	if !a.AutoInspectionEnabled || !a.Available || s.EnabledOnly && !a.Enabled || !matchesInspectionAccountType(a, s.AccountType) {
		return false
	}
	member := false
	for _, id := range a.GroupIDs {
		member = member || id == s.GroupID
	}
	if !member {
		return false
	}
	if !matchesInspectionPatterns(a, s.IncludePatterns, s.ExcludePatterns) {
		return false
	}
	if s.AccountPlan == "" || s.AccountPlan == "all" {
		return true
	}
	if a.Type != "oauth" || (a.Platform != "" && a.Platform != "openai") {
		return false
	}
	label, pro := candyPlan(a.PlanType)
	return s.AccountPlan == label || s.AccountPlan == "Pro ALL" && pro
}

// Keep these labels aligned with CODEX_PLANS in web/components.js.
func candyPlan(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	switch strings.ToLower(id) {
	case "promax":
		return "Pro 500", true
	case "pro":
		return "Pro 200", true
	case "prolite", "pro-lite", "pro_lite":
		return "Pro 100", true
	case "plus":
		return "Plus", false
	case "go":
		return "Go", false
	case "self_serve_business_prolite":
		return "Business Premium", false
	case "team", "self_serve_business_usage_based":
		return "Business", false
	case "business", "enterprise", "ent26", "enterprise_cbp_usage_based", "hc":
		return "Enterprise", false
	case "enterprise_cbp_automation":
		return "Enterprise (Automation)", false
	case "edu_pro":
		return "Edu Pro", false
	case "edu_plus":
		return "Edu Plus", false
	case "edu", "education":
		return "Edu", false
	case "free":
		return "Free", false
	}
	if id == "" {
		id = "未知"
	}
	return id, false
}

func (i *inspector) run(ctx context.Context, s InspectionSettings, done chan struct{}) {
	testType := inspectionTestType(s)
	defer func() {
		i.mu.Lock()
		now := time.Now().UTC()
		i.state.FinishedAt = &now
		if ctx.Err() != nil {
			i.state.Phase = "cancelled"
			for n := range i.state.Results {
				r := &i.state.Results[n]
				if r.Outcome == "testing" || r.Outcome == "recommended" {
					r.Outcome = "cancelled"
					r.TargetPriority = nil
				}
			}
		} else if i.state.Error != "" {
			i.state.Phase = "failed"
		} else {
			i.state.Phase = "completed"
		}
		i.state.Running, i.state.Cancelling = false, false
		i.runCancel = nil
		i.scheduleLocked()
		i.mu.Unlock()
		close(done)
		i.signal()
	}()
	accounts, err := i.hooks.List(ctx, s)
	if err != nil {
		i.mu.Lock()
		i.state.Error = err.Error()
		i.mu.Unlock()
		return
	}
	eligible := []InspectionAccount{}
	for _, a := range accounts {
		if !inspectionEligible(a, s) {
			continue
		}
		i.engine.mu.Lock()
		busy := i.engine.active[strconv.FormatInt(a.AccountID, 10)] != nil
		i.engine.mu.Unlock()
		if busy {
			continue
		}
		a.GroupID = s.GroupID
		eligible = append(eligible, a)
	}
	i.mu.Lock()
	i.state.Total = len(eligible)
	for _, a := range eligible {
		i.state.Results = append(i.state.Results, InspectionResult{AccountID: a.AccountID, AccountName: a.Name, Priority: a.Priority, RunIDs: []string{}, Outcome: "testing"})
	}
	i.mu.Unlock()
	groups := map[int][]InspectionAccount{s.DegradedPriority: {}, s.NormalPriority: {}}
	indexes := map[int64]int{}
	baselines := map[int64]int{}
	for n, a := range eligible {
		indexes[a.AccountID] = n
	}
	changed := map[int][]int{}
	apply := func(a InspectionAccount, target int) {
		n := indexes[a.AccountID]
		if i.applyAccount(ctx, s, n, a, baselines[a.AccountID], target) {
			changed[target] = append(changed[target], n)
		}
	}
	first := i.testPhase(ctx, s, eligible, 1, false, func(a InspectionAccount, answers []bool) {
		n := indexes[a.AccountID]
		i.result(n, func(r *InspectionResult) { r.Outcome = "unchanged" })
		current, err := i.hooks.Get(ctx, a.AccountID)
		if err != nil {
			i.result(n, func(r *InspectionResult) { r.Outcome = "error"; r.Error = err.Error() })
			return
		}
		if !inspectionEligible(current, s) {
			return
		}
		a.Priority = current.Priority
		baselines[a.AccountID] = current.Priority
		target := s.DegradedPriority
		if answers[0] {
			target = s.NormalPriority
		}
		if (current.Verdict == InspectionVerdictDegraded) == !answers[0] {
			apply(a, target)
			return
		}
		if testType == "fingerprint" {
			i.result(n, func(r *InspectionResult) { p := target; r.TargetPriority = &p; r.Outcome = "recommended" })
			apply(a, target)
			return
		}
		groups[target] = append(groups[target], a)
	})
	if ctx.Err() != nil {
		return
	}
	for _, target := range []int{s.DegradedPriority, s.NormalPriority} {
		group := groups[target]
		if len(group) == 0 {
			continue
		}
		if target == s.NormalPriority && s.MaxTestsPerAccount > 1 {
			i.phase("recovery_wait")
			if !waitInspection(ctx, i.recovery) {
				return
			}
		}
		confirmed := func(a InspectionAccount, confirmation []bool) {
			n := indexes[a.AccountID]
			answers := append(append([]bool{}, first[a.AccountID]...), confirmation...)
			if len(answers) != s.MaxTestsPerAccount {
				return
			}
			expected := target == s.NormalPriority
			consistent := true
			for _, answer := range answers {
				consistent = consistent && answer == expected
			}
			if !consistent {
				i.result(n, func(r *InspectionResult) { r.Outcome = "unchanged" })
				return
			}
			i.result(n, func(r *InspectionResult) { p := target; r.TargetPriority = &p; r.Outcome = "recommended" })
			apply(a, target)
		}
		if s.MaxTestsPerAccount > 1 {
			i.phase("testing")
			i.testPhase(ctx, s, group, s.MaxTestsPerAccount-1, true, confirmed)
		} else {
			for _, a := range group {
				confirmed(a, nil)
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	testLabel := "糖果测试"
	if testType == "fingerprint" {
		testLabel = "指纹检测"
	}
	for _, target := range []int{s.NormalPriority, s.DegradedPriority} {
		indexes := changed[target]
		if !s.NotifyEnabled || i.hooks.Notify == nil || len(indexes) == 0 || ctx.Err() != nil {
			continue
		}
		title := "账号确认降智"
		verdict := "正常 → 降级"
		if target == s.NormalPriority {
			title = "账号恢复正常"
			verdict = "降级 → 正常"
		}
		lines := []string{fmt.Sprintf("%s · %s\n确认状态：%s", testLabel, s.Model, verdict)}
		snapshot := i.snapshot()
		for _, n := range indexes {
			r := snapshot.Results[n]
			lines = append(lines, fmt.Sprintf("%s：%s", r.AccountName, verdict))
		}
		if err := i.hooks.Notify(ctx, s, "Sub2API 巡检："+title, strings.Join(lines, "\n")); err != nil {
			for _, n := range indexes {
				i.result(n, func(r *InspectionResult) { r.NotificationError = err.Error() })
			}
		}
	}
	i.mu.Lock()
	i.state.Done = i.state.Total
	i.mu.Unlock()
}

// applyAccount returns whether a verdict transition was saved, independently of priority writes.
func (i *inspector) applyAccount(ctx context.Context, s InspectionSettings, n int, a InspectionAccount, baseline, target int) bool {
	current, err := i.hooks.Get(ctx, a.AccountID)
	if err == nil && !current.AutoInspectionEnabled {
		i.result(n, func(r *InspectionResult) { r.Outcome = "unchanged"; r.TargetPriority = nil })
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	changed := false
	if err == nil {
		verdict := InspectionVerdictDegraded
		if target == s.NormalPriority {
			verdict = InspectionVerdictNormal
		}
		changed = (current.Verdict == InspectionVerdictDegraded) != (verdict == InspectionVerdictDegraded)
		err = i.hooks.PersistVerdict(ctx, a.AccountID, verdict, s)
	}
	if err != nil {
		i.result(n, func(r *InspectionResult) { r.Outcome = "error"; r.Error = err.Error() })
		return false
	}
	if !s.AdjustPriority {
		return changed
	}
	if !inspectionEligible(current, s) || current.Priority != baseline {
		err = fmt.Errorf("账户状态或优先级已改变")
	}
	i.engine.mu.Lock()
	busy := i.engine.active[strconv.FormatInt(a.AccountID, 10)] != nil
	i.engine.mu.Unlock()
	if err == nil && busy {
		err = fmt.Errorf("账户正在测试")
	}
	if err == nil && current.Priority == target {
		return changed
	}
	if err == nil && ctx.Err() == nil {
		err = i.hooks.UpdatePriority(ctx, a.AccountID, target)
	}
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		i.result(n, func(r *InspectionResult) { r.Outcome = "error"; r.Error = err.Error() })
		return changed
	}
	i.result(n, func(r *InspectionResult) { p := target; r.TargetPriority = &p; r.Outcome = "updated" })
	return changed
}

func waitInspection(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (i *inspector) testPhase(ctx context.Context, s InspectionSettings, accounts []InspectionAccount, runs int, confirmation bool, completed func(InspectionAccount, []bool)) map[int64][]bool {
	testType := inspectionTestType(s)
	answers := map[int64][]bool{}
	accumulated := map[int64][]bool{}
	failed := map[int64]bool{}
	index := map[int64]int{}
	byID := map[int64]InspectionAccount{}
	i.mu.Lock()
	for n, r := range i.state.Results {
		index[r.AccountID] = n
	}
	i.mu.Unlock()
	for _, a := range accounts {
		byID[a.AccountID] = a
	}
	batches := 1
	phaseCtx, cancel := context.WithTimeout(ctx, i.phaseLimit)
	defer cancel()
	currentAccounts := append([]InspectionAccount(nil), accounts...)
	for step := 0; step < batches && len(currentAccounts) > 0; step++ {
		if ctx.Err() != nil {
			break
		}
		tracked := map[int64]string{}
		for _, a := range currentAccounts {
			if ctx.Err() != nil {
				break
			}
			check := func(checkCtx context.Context, id int64) error {
				current, err := i.hooks.Get(checkCtx, id)
				if err == nil && !current.AutoInspectionEnabled {
					i.result(index[id], func(r *InspectionResult) { r.Outcome = "unchanged"; r.TargetPriority = nil })
					return errInspectionStopped
				}
				if err == nil && (!inspectionEligible(current, s) || confirmation && current.Priority != a.Priority) {
					err = fmt.Errorf("账户状态或优先级已改变")
				}
				if err != nil {
					i.result(index[id], func(r *InspectionResult) { r.Outcome = "error"; r.Error = err.Error() })
				}
				return err
			}
			i.result(index[a.AccountID], func(r *InspectionResult) { r.Outcome = "testing" })
			var req StartRequest
			if testType == "fingerprint" {
				req = StartRequest{Type: "fingerprint", Targets: []Target{a.Target}, Model: s.Model, Concurrency: 1}
			} else {
				req = StartRequest{Type: "candy", Targets: []Target{a.Target}, Model: s.Model, Effort: s.Effort, Runs: runs}
			}
			summary, err := i.engine.startChecked(ctx, req, true, check)
			if err == nil && len(summary.Started) != 1 {
				err = fmt.Errorf("账户忙碌或不可测试")
				if len(summary.Unavailable) > 0 {
					err = fmt.Errorf("%s", summary.Unavailable[0].Error)
				}
			}
			if err != nil {
				failed[a.AccountID] = true
				i.result(index[a.AccountID], func(r *InspectionResult) { r.Outcome = "error"; r.Error = err.Error() })
				continue
			}
			id := summary.Started[0].ID
			tracked[a.AccountID] = id
			i.result(index[a.AccountID], func(r *InspectionResult) { r.RunIDs = append(r.RunIDs, id) })
		}
		collect := func(id int64, runID string) {
			if testType == "fingerprint" {
				i.engine.mu.Lock()
				results := []fingerprintResult{}
				for _, r := range i.engine.fingerprintResults[strconv.FormatInt(id, 10)] {
					if r.RunID == runID {
						results = append(results, r)
					}
				}
				i.engine.mu.Unlock()
				valid := len(results) == 1 && results[0].Attribution != nil && (results[0].Status == "completed" || results[0].Status == "partial") && results[0].Error == ""
				errorText := "指纹归因结果不完整"
				if len(results) > 0 && results[0].Error != "" {
					errorText = results[0].Error
				}
				if valid {
					ok := FingerprintModelID(results[0].Model) == FingerprintModelID(results[0].Attribution.Prediction)
					accumulated[id] = append(accumulated[id], ok)
				} else {
					failed[id] = true
				}
				i.result(index[id], func(r *InspectionResult) {
					r.Tests += len(results)
					if !valid && r.Outcome == "testing" {
						r.Outcome = "error"
						r.Error = errorText
					}
				})
				if !failed[id] && len(accumulated[id]) == runs && ctx.Err() == nil {
					out := append([]bool(nil), accumulated[id]...)
					answers[id] = out
					completed(byID[id], out)
				}
				return
			}
			i.engine.mu.Lock()
			results := []candyResult{}
			for _, r := range i.engine.candyResults[strconv.FormatInt(id, 10)] {
				if r.RunID == runID {
					results = append(results, r)
				}
			}
			i.engine.mu.Unlock()
			valid := len(results) == runs
			out := []bool{}
			errorText := "测试结果不完整"
			for _, r := range results {
				valid = valid && r.Status == "completed" && r.Error == "" && !r.Skipped
				if r.Error != "" {
					errorText = r.Error
				}
				out = append(out, r.OK)
			}
			i.result(index[id], func(r *InspectionResult) {
				r.Tests += len(results)
				if !valid && r.Outcome == "testing" {
					r.Outcome = "error"
					r.Error = errorText
				}
			})
			if valid && ctx.Err() == nil {
				answers[id] = out
				completed(byID[id], out)
			} else {
				failed[id] = true
			}
		}
		for len(tracked) > 0 {
			finished := map[int64]string{}
			i.engine.mu.Lock()
			for id, runID := range tracked {
				if r := i.engine.active[strconv.FormatInt(id, 10)]; r == nil || r.ID != runID {
					finished[id] = runID
				}
			}
			i.engine.mu.Unlock()
			if step == batches-1 {
				i.mu.Lock()
				i.state.Done = i.state.Total - len(tracked) + len(finished)
				i.mu.Unlock()
			}
			for id, runID := range finished {
				collect(id, runID)
				delete(tracked, id)
			}
			if len(tracked) == 0 {
				break
			}
			if !waitInspection(phaseCtx, i.poll) {
				for _, id := range tracked {
					i.engine.Cancel(id)
				}
				// Keep the round busy until its actual workers exit.
				for busy := true; busy; {
					busy = false
					i.engine.mu.Lock()
					for id, runID := range tracked {
						if r := i.engine.active[strconv.FormatInt(id, 10)]; r != nil && r.ID == runID {
							busy = true
						}
					}
					i.engine.mu.Unlock()
					if busy {
						time.Sleep(time.Millisecond * 10)
					}
				}
				for id := range tracked {
					i.result(index[id], func(r *InspectionResult) { r.Outcome = "error"; r.Error = phaseCtx.Err().Error() })
				}
				return answers
			}
		}
		if step+1 < batches {
			nextAccounts := make([]InspectionAccount, 0, len(currentAccounts))
			for _, a := range currentAccounts {
				if failed[a.AccountID] {
					continue
				}
				i.mu.Lock()
				testing := i.state.Results[index[a.AccountID]].Outcome == "testing"
				i.mu.Unlock()
				if testing {
					nextAccounts = append(nextAccounts, a)
				}
			}
			currentAccounts = nextAccounts
		}
	}
	return answers
}
