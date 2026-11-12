// sub2api-package: backend/internal/codexeval
package codexeval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type attemptedTestError struct{ error }

func (e attemptedTestError) RequestAttempted() bool { return true }

func modelTestResponse(text string, status int) *http.Response {
	wire := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n", text)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(wire))}
}

func TestCodexEvalRetries(t *testing.T) {
	oldSender, oldDelay := sender, retryDelay
	if retryDelay != 2*time.Second || modelAttempts != 3 {
		t.Fatal("retry defaults changed")
	}
	retryDelay = 5 * time.Millisecond
	t.Cleanup(func() { sender, retryDelay = oldSender, oldDelay })
	auth := credential{ID: "7", Target: Target{AccountID: 7}, ctx: context.Background()}
	t.Run("same turn and cumulative usage", func(t *testing.T) {
		calls := 0
		var firstBody []byte
		var firstHeaders http.Header
		var sent []time.Time
		sender = func(_ context.Context, _ int64, body []byte, headers http.Header) (*http.Response, error) {
			calls++
			sent = append(sent, time.Now())
			if calls == 1 {
				firstBody = bytes.Clone(body)
				firstHeaders = headers.Clone()
			}
			if !bytes.Equal(body, firstBody) || !reflect.DeepEqual(headers, firstHeaders) {
				t.Fatal("retry changed the turn")
			}
			answer := "unusable"
			if calls == 3 {
				answer = "accepted"
			}
			return modelTestResponse(answer, 200), nil
		}
		out, attempts, err := askCodex(auth.ctx, auth, "literal", "low", "", "prompt", answerTimeout, func(answer string) error {
			if answer != "accepted" {
				return errors.New("reject")
			}
			return nil
		}, nil)
		if err != nil || attempts != 3 || out.Answer != "accepted" || out.InputTokens != 6 || out.OutputTokens != 9 || out.ReasoningTokens != 3 {
			t.Fatalf("out=%+v attempts=%d err=%v", out, attempts, err)
		}
		if sent[1].Sub(sent[0]) < retryDelay || sent[2].Sub(sent[1]) < 2*retryDelay {
			t.Fatal("retry did not wait")
		}
	})
	for _, tc := range []struct {
		name      string
		status    int
		transport error
		want      int
	}{
		{"wrong Candy", 200, nil, 1}, {"unauthorized", 401, nil, 1}, {"quota", 429, nil, 1}, {"bad request", 400, nil, 1},
		{"setup", 0, errors.New("setup"), 1}, {"plugin before response", 0, context.DeadlineExceeded, 1},
		{"direct transport", 0, attemptedTestError{errors.New("network")}, 3}, {"timeout status", 408, nil, 3}, {"server", 503, nil, 3},
		{"unauthorized body read", 401, errors.New("read"), 1}, {"quota body read", 429, errors.New("read"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			sender = func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, tc.transport
				}
				return modelTestResponse("20", tc.status), tc.transport
			}
			_, attempts, err := askCodex(auth.ctx, auth, "literal", "low", "", "prompt", answerTimeout, nil, nil)
			if attempts != tc.want || calls != tc.want || (tc.status != 200 && err == nil) {
				t.Fatalf("attempts=%d calls=%d err=%v", attempts, calls, err)
			}
		})
	}
	t.Run("stream failure retains observed usage", func(t *testing.T) {
		calls := 0
		sender = func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.in_progress\",\"usage\":{\"input_tokens\":7}}\n\n"))}, nil
		}
		out, attempts, err := askCodex(auth.ctx, auth, "literal", "", "", "prompt", answerTimeout, nil, nil)
		if err == nil || attempts != 3 || calls != 3 || out.InputTokens != 21 {
			t.Fatalf("out=%+v attempts=%d err=%v", out, attempts, err)
		}
	})
	t.Run("cancel during backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		sender = func(context.Context, int64, []byte, http.Header) (*http.Response, error) {
			calls++
			time.AfterFunc(time.Millisecond, cancel)
			return modelTestResponse("", 503), nil
		}
		_, attempts, err := askCodex(ctx, auth, "literal", "", "", "prompt", answerTimeout, nil, nil)
		if !errors.Is(err, context.Canceled) || attempts != 1 || calls != 1 {
			t.Fatalf("attempts=%d calls=%d err=%v", attempts, calls, err)
		}
	})
}
