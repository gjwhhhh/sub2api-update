package service

import (
	"context"
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
)

const codexInspectionRemovedModels = "codex_eval_inspection_removed_models"
const codexInspectionVerdict = "codex_eval_inspection_verdict"

func (s *AccountTestService) PersistCodexInspectionVerdict(ctx context.Context, id int64, verdict codexeval.InspectionVerdict, settings codexeval.InspectionSettings) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	mapping, _ := account.Credentials["model_mapping"].(map[string]any)
	mapping = maps.Clone(mapping)
	removed := []string{}
	seen := map[string]bool{}
	switch saved := account.Extra[codexInspectionRemovedModels].(type) {
	case []string:
		removed = append(removed, saved...)
	case []any:
		for _, value := range saved {
			if name, ok := value.(string); ok {
				removed = append(removed, name)
			}
		}
	}
	changed := false
	unique := make([]string, 0, len(removed))
	for _, name := range removed {
		if !seen[name] {
			unique = append(unique, name)
			seen[name] = true
		}
	}
	removed = unique
	switch verdict {
	case codexeval.InspectionVerdictDegraded:
		if settings.ControlModels && !account.IsOpenAIPassthroughEnabled() {
			for _, name := range settings.DegradedRemoveModels {
				if target, ok := mapping[name].(string); ok && target == name {
					delete(mapping, name)
					if !seen[name] {
						removed = append(removed, name)
						seen[name] = true
					}
					changed = true
				}
			}
			if changed && len(mapping) == 0 {
				mapping["blacklist"] = "blacklist"
			}
		}
	case codexeval.InspectionVerdictNormal:
		if len(removed) > 0 {
			if mapping == nil {
				mapping = make(map[string]any)
			}
			for _, name := range removed {
				if _, exists := mapping[name]; !exists {
					mapping[name] = name
				}
			}
			if mapping["blacklist"] == "blacklist" {
				delete(mapping, "blacklist")
			}
			removed = []string{}
			changed = true
		}
	}
	if !changed {
		current := codexeval.InspectionVerdictNormal
		if account.Extra[codexInspectionVerdict] == string(codexeval.InspectionVerdictDegraded) {
			current = codexeval.InspectionVerdictDegraded
		}
		if current == verdict {
			return nil
		}
		return s.accountRepo.UpdateExtra(ctx, id, map[string]any{codexInspectionVerdict: string(verdict)})
	}
	next := *account
	next.Credentials = make(map[string]any, len(account.Credentials)+1)
	maps.Copy(next.Credentials, account.Credentials)
	next.Credentials["model_mapping"] = mapping
	next.Extra = make(map[string]any, len(account.Extra)+2)
	maps.Copy(next.Extra, account.Extra)
	next.Extra[codexInspectionRemovedModels] = removed
	next.Extra[codexInspectionVerdict] = string(verdict)
	return s.accountRepo.Update(ctx, &next)
}
