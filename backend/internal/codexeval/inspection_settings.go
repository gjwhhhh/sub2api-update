package codexeval

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

type InspectionSettings struct {
	Enabled                bool     `json:"enabled"`
	GroupID                int64    `json:"group_id"`
	AccountType            string   `json:"account_type"`
	AccountPlan            string   `json:"account_plan"`
	EnabledOnly            bool     `json:"enabled_only"`
	Model                  string   `json:"model"`
	Effort                 string   `json:"effort"`
	TestType               string   `json:"test_type"`
	MaxTestsPerAccount     int      `json:"max_tests_per_account"`
	MinWaitMinutes         int      `json:"min_wait_minutes"`
	MaxWaitMinutes         int      `json:"max_wait_minutes"`
	IncludePatterns        []string `json:"include_patterns"`
	ExcludePatterns        []string `json:"exclude_patterns"`
	AdjustPriority         bool     `json:"adjust_priority"`
	ControlModels          bool     `json:"control_models"`
	NotifyEnabled          bool     `json:"notify_enabled"`
	DegradedRemoveModels   []string `json:"degraded_remove_models"`
	NormalPriority         int      `json:"normal_priority"`
	DegradedPriority       int      `json:"degraded_priority"`
	NtfyServer             string   `json:"ntfy_server"`
	NtfyTopic              string   `json:"ntfy_topic"`
	NtfyUsername           string   `json:"ntfy_username"`
	NtfyPassword           string   `json:"ntfy_password"`
	BarkURL                string   `json:"bark_url"`
	NtfyPasswordConfigured bool     `json:"ntfy_password_configured"`
	BarkURLConfigured      bool     `json:"bark_url_configured"`
}

func DefaultInspectionSettings() InspectionSettings {
	return InspectionSettings{Model: "gpt-6.1-sol", Effort: "low", TestType: "candy", MaxTestsPerAccount: 3, MinWaitMinutes: 15, MaxWaitMinutes: 35,
		AccountType: "all", AccountPlan: "all", NormalPriority: 10, DegradedPriority: 99, NotifyEnabled: true, NtfyServer: "https://ntfy.sh",
		IncludePatterns: []string{}, ExcludePatterns: []string{},
		DegradedRemoveModels: []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol", "gpt-6-astra", "gpt-6.1-terra"}}
}

func normalizePatternSlice(patterns []string) []string {
	if len(patterns) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(patterns))
	seen := make(map[string]bool, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true
		out = append(out, pattern)
	}
	return out
}

func validInspectionAccountType(accountType string) bool {
	switch accountType {
	case "all", "oauth", "apikey", "setup-token", "upstream":
		return true
	}
	platform, credType, ok := strings.Cut(accountType, ":")
	if !ok || platform == "" {
		return false
	}
	for _, r := range platform {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	switch credType {
	case "oauth", "setup-token", "apikey", "upstream":
		return true
	default:
		return false
	}
}

func (s *InspectionSettings) NormalizeModels() {
	if strings.TrimSpace(s.TestType) == "" {
		s.TestType = "candy"
	} else {
		s.TestType = strings.TrimSpace(s.TestType)
	}
	s.IncludePatterns = normalizePatternSlice(s.IncludePatterns)
	s.ExcludePatterns = normalizePatternSlice(s.ExcludePatterns)
	if s.DegradedRemoveModels == nil {
		s.DegradedRemoveModels = DefaultInspectionSettings().DegradedRemoveModels
	}
	models := make([]string, 0, len(s.DegradedRemoveModels))
	seen := make(map[string]bool)
	for _, model := range s.DegradedRemoveModels {
		model = strings.TrimSpace(model)
		if !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	s.DegradedRemoveModels = models
}

func ValidInspectionRemovalModel(model string) bool {
	return validModelName(model, true) && !strings.ContainsAny(model, "*?") && model != "blacklist"
}

func (s InspectionSettings) Public() InspectionSettings {
	s.NtfyPasswordConfigured = s.NtfyPassword != ""
	s.BarkURLConfigured = s.BarkURL != ""
	s.NtfyPassword, s.BarkURL = "", ""
	return s
}

func (s InspectionSettings) Validate() error {
	for _, model := range s.DegradedRemoveModels {
		if !ValidInspectionRemovalModel(strings.TrimSpace(model)) {
			return fmt.Errorf("请输入有效的降级移除模型")
		}
	}
	if s.GroupID < 0 || s.Enabled && s.GroupID == 0 {
		return fmt.Errorf("请选择糖果测试分组")
	}
	if !validInspectionAccountType(s.AccountType) {
		return fmt.Errorf("请选择有效账户类型")
	}
	if strings.TrimSpace(s.AccountPlan) == "" {
		return fmt.Errorf("请选择有效订阅类型")
	}
	testType := strings.TrimSpace(s.TestType)
	if testType == "" {
		testType = "candy"
	}
	if testType != "candy" && testType != "fingerprint" {
		return fmt.Errorf("请选择有效检查方式")
	}
	if !validModelName(strings.TrimSpace(s.Model), testType != "fingerprint") {
		return fmt.Errorf("请输入有效模型")
	}
	for _, pattern := range append(append([]string(nil), s.IncludePatterns...), s.ExcludePatterns...) {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("请输入有效的账户匹配通配符")
		}
	}
	if s.MaxTestsPerAccount < 1 || s.MaxTestsPerAccount > candyMaxRuns {
		return fmt.Errorf("每账户最多测试次数须为 1–%d", candyMaxRuns)
	}
	if s.MinWaitMinutes < 1 || s.MaxWaitMinutes < s.MinWaitMinutes {
		return fmt.Errorf("最长等待时间须不小于最短等待时间，且至少为 1 分钟")
	}
	// Duration conversion must remain representable.
	if int64(s.MaxWaitMinutes) > (1<<63-1)/int64(time.Minute) {
		return fmt.Errorf("等待时间过长")
	}
	if s.NormalPriority < 1 {
		return fmt.Errorf("正常优先级须至少为 1")
	}
	if s.NormalPriority >= s.DegradedPriority {
		return fmt.Errorf("正常优先级须小于降级优先级")
	}
	for _, raw := range []string{s.NtfyServer, s.BarkURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("请输入有效的 HTTP 或 HTTPS 通知地址")
		}
	}
	return nil
}
