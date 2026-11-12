package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func isInspectionSupportedAccount(a *service.Account) bool {
	if a == nil {
		return false
	}
	return a.IsOAuth() || a.Type == service.AccountTypeAPIKey || a.Type == service.AccountTypeUpstream
}

func extractAccountEmail(a *service.Account) string {
	if a == nil {
		return ""
	}
	for _, key := range []string{"email", "email_address"} {
		if a.Extra != nil {
			if v, ok := a.Extra[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		if v := strings.TrimSpace(a.GetCredential(key)); v != "" {
			return v
		}
	}
	return ""
}

func inspectionAccount(a *service.Account) codexeval.InspectionAccount {
	enabled, exists := a.Extra["codex_eval_inspection_enabled"].(bool)
	if !exists {
		enabled = true
	}
	verdict := codexeval.InspectionVerdictNormal
	if a.Extra["codex_eval_inspection_verdict"] == string(codexeval.InspectionVerdictDegraded) {
		verdict = codexeval.InspectionVerdictDegraded
	}
	return codexeval.InspectionAccount{Target: codexeval.Target{AccountID: a.ID}, Name: a.Name,
		Email: extractAccountEmail(a), Platform: a.Platform,
		Priority: a.Priority, Verdict: verdict, Available: a.IsSchedulable(), GroupIDs: a.GroupIDs, Type: a.Type,
		PlanType: a.GetCredential("plan_type"), Enabled: a.Schedulable, AutoInspectionEnabled: enabled}
}

func (h *AccountHandler) UpdateCodexInspectionAccount(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	a, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !isInspectionSupportedAccount(a) {
		response.BadRequest(c, "Account does not support Candy inspection")
		return
	}
	if err := h.adminService.UpdateAccountExtra(c.Request.Context(), id, map[string]any{"codex_eval_inspection_enabled": *req.Enabled}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": id, "enabled": *req.Enabled})
}

func (h *AccountHandler) inspectionAccounts(ctx context.Context, items []service.Account) []codexeval.InspectionAccount {
	rows := make([]AccountWithConcurrency, len(items))
	for n := range items {
		rows[n].Account = h.accountListResponseFromService(&items[n])
	}
	h.enrichShadowParents(ctx, rows)
	out := make([]codexeval.InspectionAccount, len(items))
	for n := range items {
		out[n] = inspectionAccount(&items[n])
		if out[n].PlanType == "" {
			out[n].PlanType = rows[n].ParentPlanType
		}
	}
	return out
}

func (h *AccountHandler) persistInspectionVerdict(ctx context.Context, id int64, verdict codexeval.InspectionVerdict, settings codexeval.InspectionSettings) error {
	return h.accountTestService.PersistCodexInspectionVerdict(ctx, id, verdict, settings)
}

func (h *SettingHandler) InitializeCodexInspection(accounts *AccountHandler) {
	codexeval.InitializeInspection(codexeval.InspectionHooks{
		Load: h.settingService.GetCodexInspectionSettings,
		List: func(ctx context.Context, settings codexeval.InspectionSettings) ([]codexeval.InspectionAccount, error) {
			out := []codexeval.InspectionAccount{}
			for page := 1; ; page++ {
				items, total, err := accounts.adminService.ListAccounts(ctx, page, 100, "", "", "", "", settings.GroupID, "", "id", "asc")
				if err != nil {
					return nil, err
				}
				resolved := accounts.inspectionAccounts(ctx, items)
				for n := range items {
					a := &items[n]
					if isInspectionSupportedAccount(a) {
						out = append(out, resolved[n])
					}
				}
				if page*100 >= int(total) || len(items) == 0 {
					break
				}
			}
			return out, nil
		},
		Get: func(ctx context.Context, id int64) (codexeval.InspectionAccount, error) {
			a, err := accounts.adminService.GetAccount(ctx, id)
			if err != nil {
				return codexeval.InspectionAccount{}, err
			}
			return accounts.inspectionAccounts(ctx, []service.Account{*a})[0], nil
		},
		PersistVerdict: accounts.persistInspectionVerdict,
		UpdatePriority: func(ctx context.Context, id int64, priority int) error {
			_, err := accounts.adminService.UpdateAccount(ctx, id, &service.UpdateAccountInput{Priority: &priority})
			return err
		},
		Notify: codexeval.SendInspectionNotification,
	})
}

func (h *SettingHandler) GetCodexInspectionSettings(c *gin.Context) {
	s, err := h.settingService.GetCodexInspectionSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, s.Public())
}
func (h *SettingHandler) UpdateCodexInspectionSettings(c *gin.Context) {
	var req codexeval.InspectionSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	req.NormalizeModels()
	if err := req.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	s, err := h.settingService.UpdateCodexInspectionSettings(c.Request.Context(), req)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, s.Public())
}
func (h *AccountHandler) CodexInspectionState(c *gin.Context) {
	response.Success(c, codexeval.CurrentInspectionState())
}
func (h *AccountHandler) CodexInspectionStart(c *gin.Context) {
	if err := codexeval.StartInspection(); err != nil {
		response.Error(c, http.StatusConflict, err.Error())
		return
	}
	response.Success(c, codexeval.CurrentInspectionState())
}
func (h *AccountHandler) CodexInspectionStop(c *gin.Context) {
	codexeval.CancelInspection()
	response.Success(c, codexeval.CurrentInspectionState())
}
