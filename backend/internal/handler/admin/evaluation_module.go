package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/codexeval"
)

// EvaluationModule is the host boundary for the source-integrated evaluator.
// Construction wires dependencies; Start and Stop belong to server lifecycle.
type EvaluationModule struct {
	Engine   *codexeval.Engine
	settings *SettingHandler
	accounts *AccountHandler
}

func NewEvaluationModule(dataDir string, accounts *AccountHandler, settings *SettingHandler) *EvaluationModule {
	engine := codexeval.New(dataDir, accounts.accountTestService.SendCodexEvaluation, accounts.accountTestService.CodexEvalAccount)
	accounts.SetEvaluationEngine(engine)
	settings.evaluation = engine
	return &EvaluationModule{Engine: engine, settings: settings, accounts: accounts}
}
func (m *EvaluationModule) Start()                                     { m.settings.InitializeCodexInspection(m.accounts) }
func (m *EvaluationModule) Stop(ctx context.Context) error             { return m.Engine.Stop(ctx) }
func (h *AccountHandler) SetEvaluationEngine(engine *codexeval.Engine) { h.evaluation = engine }
