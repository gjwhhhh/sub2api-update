package codexeval

import (
	"errors"
	"time"
)

const candyMaxRuns = 10

const candyPrompt = `不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）

        苹果味  桃子味  西瓜味
圆形       7      9      8
五角星形   7      6      4
`

type candyResult struct {
	Info
	Status          string    `json:"status"`
	Skipped         bool      `json:"skipped,omitempty"`
	Time            time.Time `json:"time"`
	Model           string    `json:"model"`
	Effort          string    `json:"effort"`
	OK              bool      `json:"ok"`
	Answer          string    `json:"answer,omitempty"`
	Error           string    `json:"error,omitempty"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	DurationMS      int64     `json:"duration_ms"`
	Attempts        int       `json:"attempts,omitempty"`
}

type candyProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Each credential runs serially; different credentials run in parallel.
func (engine *Engine) runCandyChecked(auth credential, model, effort string, runs int, check func() error) {
	id := auth.ID
	defer func() {
		engine.mu.Lock()
		delete(engine.candyRunning, id)
		engine.mu.Unlock()
	}()
	for range runs {
		engine.mu.Lock()
		stopping := auth.ctx.Err() != nil
		engine.mu.Unlock()
		if stopping {
			return
		}
		r, save := engine.evaluateCandyChecked(auth, model, effort, check)
		if !save {
			return
		}
		engine.mu.Lock()
		engine.candyResults[id] = appendRecent(engine.candyResults[id], r)
		engine.candyRunning[id].Done++
		_ = engine.saveStateLocked()
		engine.mu.Unlock()
	}
}

var errInspectionStopped = errors.New("inspection disabled")

func (engine *Engine) evaluateCandyChecked(auth credential, model, effort string, check func() error) (candyResult, bool) {
	r := candyResult{Info: auth.info(), Status: "completed", Time: time.Now().UTC(), Model: model, Effort: effort}
	started := time.Now()
	out, attempts, err := engine.askCodex(auth.ctx, auth, model, effort, "", candyPrompt, answerTimeout, nil, check)
	if errors.Is(err, errInspectionStopped) {
		return r, false
	}
	r.Attempts = attempts
	r.DurationMS = time.Since(started).Milliseconds()
	r.InputTokens, r.OutputTokens, r.ReasoningTokens = out.InputTokens, out.OutputTokens, out.ReasoningTokens
	if err != nil {
		r.Error = truncate(err.Error(), 500)
		r.Status = failedStatus(auth.ctx)
		return r, true
	}
	r.Answer = truncate(out.Answer, 4000)
	r.OK = hasStandalone21(out.Answer)
	return r, true
}

// hasStandalone21 reports whether "21" appears without adjacent digits.
func hasStandalone21(text string) bool {
	isDigit := func(i int) bool { return i >= 0 && i < len(text) && text[i] >= '0' && text[i] <= '9' }
	for i := 0; i+1 < len(text); i++ {
		if text[i] == '2' && text[i+1] == '1' && !isDigit(i-1) && !isDigit(i+2) {
			return true
		}
	}
	return false
}
