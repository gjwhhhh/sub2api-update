package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
)

const codexInspectionSettingPrefix = "codex_eval_inspection_"

func inspectionSettingValues(s codexeval.InspectionSettings) map[string]json.RawMessage {
	raw, _ := json.Marshal(s)
	values := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &values)
	delete(values, "ntfy_password_configured")
	delete(values, "bark_url_configured")
	return values
}

func (s *SettingService) GetCodexInspectionSettings(ctx context.Context) (codexeval.InspectionSettings, error) {
	out := codexeval.DefaultInspectionSettings()
	fields := inspectionSettingValues(out)
	keys := make([]string, 0, len(fields))
	for field := range fields {
		keys = append(keys, codexInspectionSettingPrefix+field)
	}
	stored, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return out, err
	}
	for field := range fields {
		if value, ok := stored[codexInspectionSettingPrefix+field]; ok {
			if (field == "include_patterns" || field == "exclude_patterns" || field == "test_type") && !json.Valid([]byte(value)) {
				continue
			}
			fields[field] = json.RawMessage(value)
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return out, fmt.Errorf("Read inspection settings: %w", err)
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("Read inspection settings: %w", err)
	}
	if out.NormalPriority < 1 {
		out.NormalPriority = codexeval.DefaultInspectionSettings().NormalPriority
	}
	out.NormalizeModels()
	return out, nil
}

func (s *SettingService) UpdateCodexInspectionSettings(ctx context.Context, next codexeval.InspectionSettings) (codexeval.InspectionSettings, error) {
	next.NormalizeModels()
	current, err := s.GetCodexInspectionSettings(ctx)
	if err != nil {
		return current, err
	}
	if next.NtfyPassword == "" {
		next.NtfyPassword = current.NtfyPassword
	}
	if next.BarkURL == "" {
		next.BarkURL = current.BarkURL
	}
	if err = next.Validate(); err != nil {
		return current, err
	}
	values := map[string]string{}
	for field, value := range inspectionSettingValues(next) {
		values[codexInspectionSettingPrefix+field] = string(value)
	}
	if err = s.settingRepo.SetMultiple(ctx, values); err != nil {
		return current, err
	}
	codexeval.UpdateInspectionSettings(next)
	return next, nil
}
