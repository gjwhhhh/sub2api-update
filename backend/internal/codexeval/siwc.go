package codexeval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const siwcTimeout = time.Minute

type siwcResult struct {
	Info
	ID              string    `json:"id"`
	Time            time.Time `json:"time"`
	Model           string    `json:"model"`
	Status          string    `json:"status"`
	Code            string    `json:"code,omitempty"`
	HTTPStatus      int       `json:"http_status,omitempty"`
	Error           string    `json:"error,omitempty"`
	DurationMS      int64     `json:"duration_ms"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
}

func (r siwcResult) summary() siwcResult {
	return r
}

var (
	siwcResults = map[string][]siwcResult{}
	siwcRunning = map[string]bool{}
)

func runSIWC(auth credential, model string) {
	r := evaluateSIWC(auth, model)
	mu.Lock()
	defer mu.Unlock()
	siwcResults[auth.ID] = appendRecent(siwcResults[auth.ID], r)
	delete(siwcRunning, auth.ID)
	_ = saveStateLocked()
}

func evaluateSIWC(auth credential, model string) (r siwcResult) {
	now := time.Now()
	r = siwcResult{
		Info:   auth.info(),
		ID:     fmt.Sprint(now.UnixNano()),
		Time:   now.UTC(),
		Model:  model,
		Status: "failed",
	}
	body := map[string]any{
		"model": model, "store": false, "stream": true,
		"input": []map[string]string{{"role": "user", "content": "Reply with OK."}},
	}
	headers := http.Header{
		"Content-Type": {"application/json"},
		"Accept":       {"text/event-stream"},
		"User-Agent":   {"cpa-siwc-check/" + pluginVersion},
	}
	requestCtx, cancel := withModelTimeout(auth.ctx, siwcTimeout)
	out, status, err := executeResponse(requestCtx, auth, model, body, headers)
	cancel()
	r.DurationMS = time.Since(now).Milliseconds()
	r.InputTokens = out.Usage.InputTokens
	r.OutputTokens = out.Usage.OutputTokens
	r.ReasoningTokens = out.Usage.OutputTokensDetails.ReasoningTokens
	if auth.ctx.Err() != nil {
		r.Status = "cancelled"
		r.Error = "检测已取消"
		return
	}
	if err == nil && out.completed() {
		r.Status = "available"
		return
	}
	if status != http.StatusOK && status > 0 {
		r.HTTPStatus = status
	}
	message := "模型响应未完成"
	if err != nil {
		message = err.Error()
	} else if len(out.Error) > 0 && string(out.Error) != "null" {
		message = string(out.Error)
	}
	r.Code, r.Error = siwcErrorDetails(message)
	switch {
	case r.Code == "subscription_sharing_user_not_eligible":
		r.Status = "restricted"
	case r.Code == "subscription_sharing_usage_limit_exceeded":
		r.Status = "limited"
	case r.Code == "subscription_sharing_invalid_user" || r.HTTPStatus == http.StatusUnauthorized:
		r.Status = "auth_error"
	case r.HTTPStatus == http.StatusTooManyRequests:
		r.Status = "limited"
	}
	if r.Error == "" {
		r.Error = truncate(message, 500)
	}
	return
}

func siwcErrorDetails(message string) (code, text string) {
	start := strings.IndexByte(message, '{')
	if start < 0 {
		return "", truncate(message, 500)
	}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(message[start:]), &parsed) != nil {
		return "", truncate(message, 500)
	}
	code = truncate(firstText(parsed.Error.Code, parsed.Code), 120)
	text = truncate(firstText(parsed.Error.Message, parsed.Message, message), 500)
	return
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
