//go:build unit

package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type evaluationUsers struct {
	service.UserRepository
	role string
}

type evaluationSettings struct{ scopedRouteModelSettings }

func (*evaluationSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

func (r *evaluationUsers) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 9, Role: r.role, Status: service.StatusActive}, nil
}
func (*evaluationUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestEvaluationRouteAuthorizationAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	paths := []struct{ method, path string }{
		{"GET", "/codex-eval/state"}, {"POST", "/codex-eval/start"}, {"POST", "/codex-eval/cancel"},
		{"GET", "/codex-eval/records/candy/7/record"}, {"POST", "/codex-eval/clear-history"},
		{"GET", "/codex-eval/inspection/state"}, {"POST", "/codex-eval/inspection/start"}, {"POST", "/codex-eval/inspection/stop"},
		{"POST", "/accounts/7/codex-eval"}, {"GET", "/accounts/7/codex-eval/models"}, {"PUT", "/accounts/7/codex-eval-inspection"},
		{"GET", "/settings/codex-eval-inspection"}, {"PUT", "/settings/codex-eval-inspection"},
	}
	for _, role := range []string{service.RoleUser, service.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			settings := service.NewSettingService(&evaluationSettings{}, nil)
			keys := &scopedRouteKeys{}
			settings.SetScopedAdminKeyRepository(keys)
			key, _, err := settings.CreateScopedAdminKey(context.Background(), service.ScopedAdminKeyInput{Name: "evaluation scope", Enabled: true,
				Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.read"}, GroupIDs: []int64{1}}}}}, 9)
			require.NoError(t, err)
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "evaluation-test-secret", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			token, err := auth.GenerateToken(context.Background(), &service.User{ID: 9, Role: role})
			require.NoError(t, err)
			account := adminhandler.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			engine := codexeval.New(t.TempDir(), nil, nil)
			account.SetEvaluationEngine(engine)
			t.Cleanup(func() { require.NoError(t, engine.Stop(context.Background())) })
			r := gin.New()
			RegisterAdminRoutes(r.Group("/api/v1"), &handler.Handlers{Admin: &handler.AdminHandlers{Account: account}},
				middleware.NewAdminAuthMiddleware(auth, service.NewUserService(&evaluationUsers{role: role}, nil, nil, nil), settings, nil),
				middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
				middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.AbortWithStatusJSON(403, gin.H{"reason": "STEP_UP_REQUIRED"}) }), nil, nil)
			request := func(method, path, header, value string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/api/v1/admin"+path, strings.NewReader(`{"scope":"all"}`))
				req.Header.Set("Content-Type", "application/json")
				if header != "" {
					req.Header.Set(header, value)
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				return w
			}
			for _, p := range paths {
				require.Equal(t, 401, request(p.method, p.path, "", "").Code, p.path)
				want := 403
				if role == service.RoleUser {
					want = 401
				} // scoped keys require an active administrator creator
				require.Equal(t, want, request(p.method, p.path, "x-api-key", key).Code, p.path)
				if role == service.RoleUser {
					require.Equal(t, 403, request(p.method, p.path, "Authorization", "Bearer "+token).Code, p.path)
				}
			}
			if role == service.RoleAdmin {
				w := request("GET", "/codex-eval/state", "Authorization", "Bearer "+token)
				require.Equal(t, 200, w.Code, w.Body.String())
				for _, p := range []struct{ method, path string }{{"POST", "/codex-eval/clear-history"}, {"PUT", "/settings/codex-eval-inspection"}} {
					w := request(p.method, p.path, "Authorization", "Bearer "+token)
					require.Equal(t, 403, w.Code)
					require.Contains(t, w.Body.String(), "STEP_UP_REQUIRED")
				}
				keys.item.Policy = adminaccess.Policy{FullAccess: true}
				require.Equal(t, 200, request("GET", "/codex-eval/state", "x-api-key", key).Code)
			}
		})
	}
}
