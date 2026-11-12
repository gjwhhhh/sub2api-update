package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Evaluation uses the admin group's authentication, audit and compliance checks.
func registerEvaluationRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUp middleware.StepUpAuthMiddleware) {
	eval := admin.Group("/codex-eval")
	eval.GET("/state", h.Admin.Account.CodexEvalState)
	eval.POST("/start", h.Admin.Account.CodexEvalStart)
	eval.POST("/cancel", h.Admin.Account.CodexEvalCancel)
	eval.GET("/records/:type/:account_id/:record_id", h.Admin.Account.CodexEvalRecord)
	eval.POST("/clear-history", gin.HandlerFunc(stepUp), h.Admin.Account.CodexEvalClear)
	eval.GET("/inspection/state", h.Admin.Account.CodexInspectionState)
	eval.POST("/inspection/start", h.Admin.Account.CodexInspectionStart)
	eval.POST("/inspection/stop", h.Admin.Account.CodexInspectionStop)
}
