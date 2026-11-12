package main

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
)

func provideEvaluationModule(h *handler.Handlers, cfg *config.Config) *admin.EvaluationModule {
	return admin.NewEvaluationModule(cfg.Pricing.DataDir, h.Admin.Account, h.Admin.Setting)
}
