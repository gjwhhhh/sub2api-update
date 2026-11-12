// sub2api-package: backend/internal/service
package service

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/stretchr/testify/require"
)

type codexInspectionModelRepo struct {
	*upstreamBillingProbeAccountRepo
	updates, merges int
	fail            bool
}

func (r *codexInspectionModelRepo) Update(ctx context.Context, account *Account) error {
	r.updates++
	if r.fail {
		return errors.New("fixture account update failure")
	}
	return r.upstreamBillingProbeAccountRepo.Update(ctx, account)
}

func (r *codexInspectionModelRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.merges++
	return r.upstreamBillingProbeAccountRepo.UpdateExtra(ctx, id, updates)
}

func newCodexInspectionModelFixture(mapping map[string]any) (*AccountTestService, *codexInspectionModelRepo) {
	a := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://fixture.invalid", "model_mapping": mapping,
			"compact_model_mapping": map[string]any{"compact": "target"}},
		Extra: map[string]any{"unrelated": true, codexInspectionVerdict: "normal"}}
	r := &codexInspectionModelRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{20: a}}}
	return &AccountTestService{accountRepo: r}, r
}

func TestCodexInspectionModelTransitions(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "final entry"}[final], func(t *testing.T) {
			mapping := map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}
			if !final {
				mapping["gpt-6-sol"] = "administrator-target"
				mapping["alias"] = "gpt-6.1-sol"
				mapping["gpt-*"] = "wildcard-target"
				mapping["other"] = "other"
			}
			svc, repo := newCodexInspectionModelFixture(mapping)
			ctx := context.Background()
			s := codexeval.DefaultInspectionSettings()
			s.ControlModels = true
			before := repo.accounts[20]
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictNormal, s))
			require.Zero(t, repo.merges, "stable normal needs no initialization write")
			require.True(t, before.IsModelSupported("gpt-6.1-sol"))
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictDegraded, s))
			a := repo.accounts[20]
			require.Equal(t, 1, repo.updates)
			require.Zero(t, repo.merges)
			require.Equal(t, []string{"gpt-6.1-sol"}, a.Extra[codexInspectionRemovedModels])
			require.Equal(t, "degraded", a.Extra[codexInspectionVerdict])
			require.Equal(t, "gpt-6.1-sol", before.Credentials["model_mapping"].(map[string]any)["gpt-6.1-sol"])
			require.True(t, a.Schedulable)
			if final {
				require.Equal(t, map[string]any{"blacklist": "blacklist"}, a.Credentials["model_mapping"])
				require.False(t, a.IsModelSupported("gpt-6.1-sol"))
				require.False(t, a.IsModelSupported("other-real-model"))
			} else {
				require.Equal(t, "administrator-target", a.GetModelMapping()["gpt-6-sol"])
				require.Equal(t, "gpt-6.1-sol", a.GetModelMapping()["alias"])
				require.Equal(t, "other", a.GetModelMapping()["other"])
				require.True(t, a.IsModelSupported("gpt-6.1-sol"), "wildcards keep native admission")
				a.Credentials["model_mapping"].(map[string]any)["gpt-6.1-sol"] = "new-administrator-target"
			}
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictDegraded, s))
			require.Equal(t, 1, repo.updates, "stable matching models need no write")
			require.Zero(t, repo.merges)
			a.Credentials["model_mapping"].(map[string]any)["gpt-6-sol"] = "gpt-6-sol"
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictDegraded, s))
			a = repo.accounts[20]
			require.Equal(t, 2, repo.updates, "stable degradation must apply newly available identity entries")
			require.Equal(t, []string{"gpt-6.1-sol", "gpt-6-sol"}, a.Extra[codexInspectionRemovedModels])
			a.Credentials["model_mapping"].(map[string]any)["gpt-6-sol"] = "gpt-6-sol"
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictDegraded, s))
			a = repo.accounts[20]
			require.Equal(t, []string{"gpt-6.1-sol", "gpt-6-sol"}, a.Extra[codexInspectionRemovedModels], "repeated removals must not duplicate saved names")
			// Native JSON reads return an interface slice.
			a.Extra[codexInspectionRemovedModels] = []any{"gpt-6.1-sol", "gpt-6-sol"}
			if !final {
				a.Extra[codexInspectionVerdict] = "normal"
			}
			s.ControlModels = false
			s.DegradedRemoveModels = []string{"changed-global-model"}
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictNormal, s))
			a = repo.accounts[20]
			require.Equal(t, 4, repo.updates)
			require.Empty(t, a.Extra[codexInspectionRemovedModels])
			require.Equal(t, "normal", a.Extra[codexInspectionVerdict])
			require.NotContains(t, a.GetModelMapping(), "blacklist")
			if final {
				require.Equal(t, "gpt-6.1-sol", a.GetModelMapping()["gpt-6.1-sol"])
			} else {
				require.Equal(t, "new-administrator-target", a.GetModelMapping()["gpt-6.1-sol"])
			}
			require.True(t, a.IsModelSupported("gpt-6.1-sol"))
			require.Equal(t, "fixture-key", a.Credentials["api_key"])
			require.Equal(t, "https://fixture.invalid", a.Credentials["base_url"])
			require.Equal(t, map[string]any{"compact": "target"}, a.Credentials["compact_model_mapping"])
			require.Equal(t, true, a.Extra["unrelated"])
			require.NoError(t, svc.PersistCodexInspectionVerdict(ctx, 20, codexeval.InspectionVerdictNormal, s))
			require.Equal(t, 4, repo.updates)
			require.Zero(t, repo.merges, "matching restored state needs no write")
		})
	}
}

func TestCodexInspectionModelSkips(t *testing.T) {
	for _, kind := range []string{"absent", "unrestricted", "passthrough", "disabled", "empty list"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo := newCodexInspectionModelFixture(map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"})
			a := repo.accounts[20]
			s := codexeval.DefaultInspectionSettings()
			s.ControlModels = true
			switch kind {
			case "absent":
				s.DegradedRemoveModels = []string{"missing"}
			case "unrestricted":
				delete(a.Credentials, "model_mapping")
			case "passthrough":
				a.Extra["openai_passthrough"] = true
			case "disabled":
				s.ControlModels = false
			case "empty list":
				s.DegradedRemoveModels = []string{}
			}
			credentials := maps.Clone(a.Credentials)
			require.NoError(t, svc.PersistCodexInspectionVerdict(context.Background(), 20, codexeval.InspectionVerdictDegraded, s))
			require.Zero(t, repo.updates)
			require.Equal(t, 1, repo.merges)
			require.Equal(t, credentials, a.Credentials)
			require.NotContains(t, a.Extra, codexInspectionRemovedModels)
			require.Equal(t, "degraded", a.Extra[codexInspectionVerdict])
		})
	}
}

func TestCodexInspectionModelWriteFailure(t *testing.T) {
	svc, repo := newCodexInspectionModelFixture(map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"})
	repo.fail = true
	s := codexeval.DefaultInspectionSettings()
	s.ControlModels = true
	require.ErrorContains(t, svc.PersistCodexInspectionVerdict(context.Background(), 20, codexeval.InspectionVerdictDegraded, s), "fixture account update failure")
	a := repo.accounts[20]
	require.Equal(t, map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}, a.Credentials["model_mapping"])
	require.Equal(t, "normal", a.Extra[codexInspectionVerdict])
	require.NotContains(t, a.Extra, codexInspectionRemovedModels)
	require.Zero(t, repo.merges)
}
