// sub2api-package: backend/internal/handler/admin
package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type inspectionShadowAdminService struct {
	*stubAdminService
	parent  *service.Account
	lookups int
}

func (s *inspectionShadowAdminService) GetAccountsByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	s.lookups++
	if len(ids) != 1 || ids[0] != s.parent.ID {
		panic("unexpected parent lookup")
	}
	return []*service.Account{s.parent}, nil
}

func TestCodexInspectionShadowParentPlan(t *testing.T) {
	parentID := int64(10)
	svc := &inspectionShadowAdminService{stubAdminService: newStubAdminService(), parent: &service.Account{
		ID: parentID, Credentials: map[string]any{"plan_type": "pro"},
	}}
	h := &AccountHandler{adminService: svc}
	shadow := service.Account{ID: 20, ParentAccountID: &parentID, Type: service.AccountTypeOAuth,
		GroupIDs: []int64{101}, Status: service.StatusActive, Schedulable: true, Priority: 10}
	owned := shadow
	owned.ID, owned.Credentials = 21, map[string]any{"plan_type": "plus"}
	rows := h.inspectionAccounts(context.Background(), []service.Account{shadow, owned})
	if svc.lookups != 1 || rows[0].PlanType != "pro" || rows[1].PlanType != "plus" {
		t.Fatalf("inspection lost native plan fallback or credential precedence: %+v", rows)
	}
	if !rows[0].Available || rows[0].GroupIDs[0] != 101 || rows[0].Priority != 10 || !rows[0].AutoInspectionEnabled {
		t.Fatalf("plan enrichment changed native account state: %+v", rows[0])
	}
	// The single-account recheck must resolve the parent's current plan again.
	svc.parent.Credentials["plan_type"] = "pro_lite"
	current := h.inspectionAccounts(context.Background(), []service.Account{shadow})[0]
	if svc.lookups != 2 || current.PlanType != "pro_lite" {
		t.Fatalf("priority recheck used a stale or missing parent plan: %+v", current)
	}
	if shadow.GetCredential("plan_type") != "" {
		t.Fatal("inspection copied parent credentials into the shadow")
	}
}

type inspectionAccountAdminService struct {
	*stubAdminService
	account *service.Account
	updates map[string]any
}

func (s *inspectionAccountAdminService) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}

func (s *inspectionAccountAdminService) UpdateAccountExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.updates = updates
	for key, value := range updates {
		s.account.Extra[key] = value
	}
	return nil
}

func TestCodexInspectionAccountSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &inspectionAccountAdminService{stubAdminService: newStubAdminService(), account: &service.Account{
		ID: 20, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Extra: map[string]any{"unrelated": "preserve"}, Schedulable: true, Priority: 99,
	}}
	h := &AccountHandler{adminService: s}
	router := gin.New()
	router.PUT("/accounts/:id/codex-eval-inspection", h.UpdateCodexInspectionAccount)
	for _, body := range []string{`{}`, `{"enabled":"false"}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/accounts/20/codex-eval-inspection", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || s.updates != nil {
			t.Fatalf("invalid field accepted: %d %s", rec.Code, rec.Body)
		}
	}
	if !inspectionAccount(s.account).AutoInspectionEnabled {
		t.Fatal("absent value must default to enabled")
	}
	if inspectionAccount(s.account).Verdict != codexeval.InspectionVerdictNormal {
		t.Fatal("absent verdict must default to normal at a degraded actual priority")
	}
	s.account.Extra["email"] = "extra@example.com"
	s.account.Credentials = map[string]any{"email": "cred@example.com"}
	if inspectionAccount(s.account).Email != "extra@example.com" {
		t.Fatalf("expected Extra email precedence, got %q", inspectionAccount(s.account).Email)
	}
	delete(s.account.Extra, "email")
	if inspectionAccount(s.account).Email != "cred@example.com" {
		t.Fatalf("expected Credentials email fallback, got %q", inspectionAccount(s.account).Email)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/accounts/20/codex-eval-inspection", bytes.NewBufferString(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(s.updates) != 1 || s.updates["codex_eval_inspection_enabled"] != false || s.account.Extra["unrelated"] != "preserve" || !s.account.Schedulable || inspectionAccount(s.account).AutoInspectionEnabled {
		t.Fatalf("switch save changed unrelated state: %d %s %+v", rec.Code, rec.Body, s.account)
	}
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformGemini, service.PlatformAntigravity} {
		s.account.Platform = platform
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPut, "/accounts/20/codex-eval-inspection", bytes.NewBufferString(`{"enabled":true}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("multi-provider platform %s rejected by inspection switch: %d %s", platform, rec.Code, rec.Body)
		}
	}
}
