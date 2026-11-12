// sub2api-package: backend/internal/service
package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
)

type codexInspectionSettingsRepo struct {
	SettingRepository
	values   map[string]string
	fail     bool
	readFail bool
	keys     []string
	writes   int
}

func TestCodexInspectionParticipationSurvivesAccountEdit(t *testing.T) {
	repo := &accountBillingSettingsAdminRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		20: {ID: 20, Name: "before", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
			Extra: map[string]any{"codex_eval_inspection_enabled": false, "codex_eval_inspection_verdict": "degraded", "codex_eval_inspection_removed_models": []any{"gpt-6.1-sol"}}},
	}}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 20, &UpdateAccountInput{Name: "after", Extra: map[string]any{"unrelated": "new value"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Extra["codex_eval_inspection_enabled"] != false || updated.Extra["codex_eval_inspection_verdict"] != "degraded" || updated.Extra["unrelated"] != "new value" {
		t.Fatalf("full edit lost inspection metadata: %+v", updated.Extra)
	}
	if !reflect.DeepEqual(updated.Extra["codex_eval_inspection_removed_models"], []any{"gpt-6.1-sol"}) {
		t.Fatal("full edit lost pending model restoration")
	}
}

func (r *codexInspectionSettingsRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.keys = append([]string{}, keys...)
	if r.readFail {
		return nil, errors.New("fixture database read failure")
	}
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}
func (r *codexInspectionSettingsRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.writes++
	if r.fail {
		return errors.New("fixture database failure")
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func TestCodexInspectionSettingsLoadFailureBlocksStartUntilSaved(t *testing.T) {
	repo := &codexInspectionSettingsRepo{values: map[string]string{}, readFail: true}
	settings := &SettingService{settingRepo: repo}
	codexeval.InitializeInspection(codexeval.InspectionHooks{
		Load: settings.GetCodexInspectionSettings,
		List: func(context.Context, codexeval.InspectionSettings) ([]codexeval.InspectionAccount, error) {
			return []codexeval.InspectionAccount{}, nil
		},
	})
	defer codexeval.StopInspection()
	defer codexeval.CancelInspection()
	if err := codexeval.StartInspection(); err == nil {
		t.Fatal("inspection started before saved settings loaded")
	}
	repo.readFail = false
	next := codexeval.DefaultInspectionSettings()
	next.GroupID = 101
	if _, err := settings.UpdateCodexInspectionSettings(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err := codexeval.StartInspection(); err != nil {
		t.Fatalf("inspection did not start after settings were saved: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for codexeval.CurrentInspectionState().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := codexeval.CurrentInspectionState(); state.Running || state.Phase != "completed" {
		t.Fatalf("inspection did not complete: %+v", state)
	}
}

func TestCodexInspectionSettingsPersistence(t *testing.T) {
	ctx := context.Background()
	repo := &codexInspectionSettingsRepo{values: map[string]string{}}
	s := &SettingService{settingRepo: repo}
	settings, err := s.GetCodexInspectionSettings(ctx)
	if err != nil || settings.Enabled || !settings.NotifyEnabled || settings.MaxTestsPerAccount != 3 || settings.MinWaitMinutes != 15 || settings.MaxWaitMinutes != 35 || settings.NormalPriority != 10 {
		t.Fatalf("wrong defaults: %+v %v", settings, err)
	}
	if settings.ControlModels || !reflect.DeepEqual(settings.DegradedRemoveModels, []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol", "gpt-6-astra", "gpt-6.1-terra"}) {
		t.Fatalf("wrong model control defaults: %+v", settings)
	}
	settings.Enabled = true
	settings.ControlModels = true
	settings.NotifyEnabled = false
	settings.TestType = "fingerprint"
	settings.IncludePatterns = []string{" codex-* ", "*@example.com", "codex-*"}
	settings.ExcludePatterns = []string{" test-* "}
	settings.DegradedRemoveModels = []string{" gpt-6-sol ", "custom/model", "gpt-6-sol"}
	settings.GroupID = 202
	settings.AccountType, settings.AccountPlan, settings.EnabledOnly = "anthropic:apikey", "Pro ALL", true
	settings.NtfyPassword = "fixture-secret"
	settings.BarkURL = "https://fixture.invalid/private-key"
	if _, err = s.UpdateCodexInspectionSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	// A fresh service reads the same native setting rows.
	restarted := &SettingService{settingRepo: repo}
	saved, err := restarted.GetCodexInspectionSettings(ctx)
	if err != nil || !saved.Enabled || saved.NotifyEnabled || saved.GroupID != 202 || saved.AccountType != "anthropic:apikey" || saved.AccountPlan != "Pro ALL" || !saved.EnabledOnly || saved.TestType != "fingerprint" || saved.NtfyPassword != settings.NtfyPassword || saved.BarkURL != settings.BarkURL {
		t.Fatalf("restart lost settings: %+v %v", saved.Public(), err)
	}
	if !reflect.DeepEqual(saved.IncludePatterns, []string{"codex-*", "*@example.com"}) || !reflect.DeepEqual(saved.ExcludePatterns, []string{"test-*"}) {
		t.Fatalf("patterns did not normalize or persist: %+v", saved)
	}
	badPattern := saved.Public()
	badPattern.IncludePatterns = []string{"bad["}
	if _, err := s.UpdateCodexInspectionSettings(ctx, badPattern); err == nil {
		t.Fatal("invalid wildcard pattern was accepted")
	}
	badTestType := saved.Public()
	badTestType.TestType = "pelican"
	if _, err := s.UpdateCodexInspectionSettings(ctx, badTestType); err == nil {
		t.Fatal("invalid test_type was accepted")
	}
	public := saved.Public()
	if !saved.ControlModels || !reflect.DeepEqual(saved.DegradedRemoveModels, []string{"gpt-6-sol", "custom/model"}) {
		t.Fatalf("model controls did not normalize or persist: %+v", saved)
	}
	for _, name := range []string{"", "wild*", "wild?", "blacklist", "bad\nname"} {
		invalid := public
		invalid.DegradedRemoveModels = []string{name}
		writes := repo.writes
		if _, err := s.UpdateCodexInspectionSettings(ctx, invalid); err == nil || repo.writes != writes {
			t.Fatalf("invalid removal model was accepted: %q", name)
		}
	}
	public.DegradedRemoveModels = []string{}
	public.Model = "literal-model"
	if _, err = s.UpdateCodexInspectionSettings(ctx, public); err != nil {
		t.Fatal(err)
	}
	saved, err = s.GetCodexInspectionSettings(ctx)
	if err != nil || saved.Model != "literal-model" || saved.NtfyPassword != settings.NtfyPassword || saved.BarkURL != settings.BarkURL {
		t.Fatal("blank update lost secret")
	}
	if saved.DegradedRemoveModels == nil || len(saved.DegradedRemoveModels) != 0 {
		t.Fatal("explicit empty removal list selected defaults")
	}
	repo.fail = true
	public.Model = "unsaved"
	if _, err = s.UpdateCodexInspectionSettings(ctx, public); err == nil {
		t.Fatal("database failure hidden")
	}
	saved, _ = s.GetCodexInspectionSettings(ctx)
	if saved.Model != "literal-model" {
		t.Fatal("failed save changed settings")
	}
	invalid := codexeval.DefaultInspectionSettings()
	invalid.MinWaitMinutes = 36
	if _, err = s.UpdateCodexInspectionSettings(ctx, invalid); err == nil {
		t.Fatal("invalid range accepted")
	}
}

func TestCodexInspectionSettingsPriorityReadAndWrite(t *testing.T) {
	ctx := context.Background()
	for _, stored := range []string{"0", "-3"} {
		t.Run(stored, func(t *testing.T) {
			repo := &codexInspectionSettingsRepo{values: map[string]string{
				codexInspectionSettingPrefix + "normal_priority":   stored,
				codexInspectionSettingPrefix + "degraded_priority": "5",
				codexInspectionSettingPrefix + "include_patterns":  "invalid old value",
				codexInspectionSettingPrefix + "exclude_patterns":  `['unused']`,
			}}
			s := &SettingService{settingRepo: repo}
			settings, err := s.GetCodexInspectionSettings(ctx)
			if err != nil || settings.NormalPriority != 10 || settings.DegradedPriority != 5 || settings.Validate() == nil {
				t.Fatalf("old priority did not load the new default: %+v %v", settings.Public(), err)
			}
			if repo.writes != 0 || repo.values[codexInspectionSettingPrefix+"normal_priority"] != stored {
				t.Fatal("settings read wrote the database")
			}
			settings.NormalPriority = 0
			if _, err := s.UpdateCodexInspectionSettings(ctx, settings); err == nil || repo.writes != 0 {
				t.Fatal("new nonpositive priority was accepted or written")
			}
			settings.NormalPriority = 1
			if _, err := s.UpdateCodexInspectionSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			if repo.writes != 1 || repo.values[codexInspectionSettingPrefix+"normal_priority"] != "1" {
				t.Fatal("valid explicit save did not write the selected priority")
			}
		})
	}
}
