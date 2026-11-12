package codexeval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
)

type modelResponse struct {
	Answer          string
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
}

func validModelName(model string, allowEffort bool) bool {
	return model != "" && len(model) <= 200 && strings.IndexFunc(model, unicode.IsControl) < 0 &&
		(allowEffort || !strings.ContainsAny(model, "()"))
}

// Each attempt has its own time limit.
const (
	challengeTimeout = 3 * time.Minute
	answerTimeout    = 10 * time.Minute
	modelAttempts    = 3
)

type retryableError struct{ error }

func (e retryableError) Unwrap() error { return e.error }

func withModelTimeout(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, limit, fmt.Errorf("模型 %d 分钟内未完成回答，已中断请求", int(limit.Minutes())))
}

// askCodex reuses one turn and sums observed usage across its attempts.
func (engine *Engine) askCodex(ctx context.Context, auth credential, model, effort, instructions, prompt string, limit time.Duration, accept func(string) error, beforeSend func() error) (out modelResponse, attempts int, err error) {
	body, headers := codexTurn("sub2api-account:"+auth.ID, model, effort, instructions, prompt)
	for delay := engine.retryDelay; ; delay *= 2 {
		if err = ctx.Err(); err != nil {
			return
		}
		if beforeSend != nil {
			if err = beforeSend(); err != nil {
				return
			}
		}
		attempts++
		requestCtx, cancel := withModelTimeout(ctx, limit)
		response, _, requestErr := engine.executeModel(requestCtx, auth, model, body, headers)
		cancel()
		err = requestErr
		out.Answer = response.Answer
		out.InputTokens += response.InputTokens
		out.OutputTokens += response.OutputTokens
		out.ReasoningTokens += response.ReasoningTokens
		if err == nil && accept != nil {
			if err = accept(response.Answer); err != nil {
				err = retryableError{err}
			}
		}
		if ctx.Err() != nil {
			return out, attempts, ctx.Err()
		}
		if err == nil || attempts == modelAttempts || !errors.As(err, new(retryableError)) {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return out, attempts, ctx.Err()
		}
	}
}

// executeResponse sends a Responses request through the native administrator account sender and reads its stream.
func (engine *Engine) executeResponse(ctx context.Context, auth credential, model string, body any, headers http.Header) (out responseBody, status int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("模型请求异常：%v", recovered)
		}
	}()
	var request bytes.Buffer
	encoder := json.NewEncoder(&request)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(body); err != nil {
		return out, 0, err
	}
	response, err := engine.sender(ctx, auth.Target.AccountID, bytes.TrimSuffix(request.Bytes(), []byte("\n")), headers)
	if response != nil {
		status = response.StatusCode
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		var attempted interface{ RequestAttempted() bool }
		if status == http.StatusRequestTimeout || status >= http.StatusInternalServerError ||
			status == http.StatusOK || (status == 0 && errors.As(err, &attempted) && attempted.RequestAttempted()) {
			err = retryableError{err}
		}
		return out, status, err
	}
	stop := context.AfterFunc(ctx, func() { _ = response.Body.Close() })
	defer stop()
	status = response.StatusCode
	if status != http.StatusOK {
		raw, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			err = readErr
		} else {
			err = fmt.Errorf("API returned %d: %s", status, raw)
		}
		if status == http.StatusRequestTimeout || status >= http.StatusInternalServerError {
			err = retryableError{err}
		}
		return out, status, err
	}
	out, readErr := readResponseStream(response.Body)
	if ctx.Err() != nil {
		return out, status, context.Cause(ctx)
	}
	if readErr != nil {
		return out, status, retryableError{readErr}
	}
	return out, status, nil
}

// executeModel sends directly through the native administrator account sender and requires a completed text answer.
func (engine *Engine) executeModel(ctx context.Context, auth credential, model string, body any, headers http.Header) (result modelResponse, status int, err error) {
	out, status, err := engine.executeResponse(ctx, auth, model, body, headers)
	result.InputTokens = out.Usage.InputTokens
	result.OutputTokens = out.Usage.OutputTokens
	result.ReasoningTokens = out.Usage.OutputTokensDetails.ReasoningTokens
	if err != nil {
		return result, status, err
	}
	defer func() {
		if err != nil {
			err = retryableError{err}
		}
	}()
	if !out.completed() {
		return result, status, fmt.Errorf("模型响应未完成（%s）：%s", out.Status, out.Error)
	}
	var answer strings.Builder
	for _, item := range out.Output {
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" || part.Type == "text" {
					_, _ = answer.WriteString(part.Text)
				}
			}
		}
	}
	result.Answer = answer.String()
	if strings.TrimSpace(result.Answer) == "" {
		if slices.ContainsFunc(out.Output, func(item responseItem) bool { return strings.HasSuffix(item.Type, "_call") }) {
			return result, status, fmt.Errorf("模型调用了工具，没有直接回答")
		}
		return result, status, fmt.Errorf("模型未返回文本")
	}
	return result, status, nil
}

type responseItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type responseBody struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Output []responseItem  `json:"output"`
	Usage  struct {
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		OutputTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (r responseBody) completed() bool {
	return r.Status == "completed" && (len(r.Error) == 0 || string(r.Error) == "null")
}

// SSEData reads one event, including multi-line data and a final event without a
// trailing blank line. It ignores comments and unrelated SSE fields.
func SSEData(reader *bufio.Reader) ([]byte, error) {
	var data []byte
	for {
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			value = bytes.TrimPrefix(value, []byte(" "))
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
		if len(line) == 0 && data != nil {
			return data, nil
		}
		if err != nil {
			if err == io.EOF && data != nil {
				return data, nil
			}
			return nil, err
		}
	}
}

func readResponseStream(body io.Reader) (out responseBody, err error) {
	reader := bufio.NewReader(body)
	var items []responseItem
	final := false
	for {
		data, readErr := SSEData(reader)
		if readErr != nil {
			if readErr != io.EOF {
				return out, fmt.Errorf("模型响应流中断：%w", readErr)
			}
			break
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Usage    json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		// Snapshot updates preserve observed fields when later events omit usage.
		updateUsage := func(raw json.RawMessage) {
			var value struct {
				Input   *int64 `json:"input_tokens"`
				Output  *int64 `json:"output_tokens"`
				Details struct {
					Reasoning *int64 `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			}
			if json.Unmarshal(raw, &value) == nil {
				if value.Input != nil {
					out.Usage.InputTokens = *value.Input
				}
				if value.Output != nil {
					out.Usage.OutputTokens = *value.Output
				}
				if value.Details.Reasoning != nil {
					out.Usage.OutputTokensDetails.ReasoningTokens = *value.Details.Reasoning
				}
			}
		}
		var response struct {
			Usage json.RawMessage `json:"usage"`
		}
		_ = json.Unmarshal(event.Response, &response)
		updateUsage(response.Usage)
		updateUsage(event.Usage)
		switch event.Type {
		case "response.output_item.done":
			var item responseItem
			if json.Unmarshal(event.Item, &item) == nil {
				items = append(items, item)
			}
		case "response.completed", "response.done", "response.incomplete", "response.failed":
			usage := out.Usage
			if err = json.Unmarshal(event.Response, &out); err != nil {
				return out, fmt.Errorf("解析模型响应失败：%w", err)
			}
			out.Usage = usage
			final = true
			if out.Status == "" {
				switch event.Type {
				case "response.failed":
					out.Status = "failed"
				case "response.incomplete":
					out.Status = "incomplete"
				default:
					out.Status = "completed"
				}
			}
		case "error":
			return out, fmt.Errorf("模型返回错误：%s", data)
		}
	}
	if !final {
		return out, fmt.Errorf("模型响应流在完成前中断")
	}
	if len(out.Output) == 0 {
		out.Output = items
	}
	return out, nil
}
