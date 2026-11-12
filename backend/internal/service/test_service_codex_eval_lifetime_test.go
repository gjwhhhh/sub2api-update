//go:build unit

// sub2api-package: backend/internal/service
package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexLifetimeAccountRepo struct {
	service.AccountRepository
	account *service.Account
}

func (r codexLifetimeAccountRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id != r.account.ID {
		return nil, fmt.Errorf("unexpected account %d", id)
	}
	return r.account, nil
}

func TestCodexEvalBackgroundNativeStreamLifetime(t *testing.T) {
	t.Run("transport classification", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
		}))
		defer upstream.Close()
		account := &service.Account{ID: 917, Name: "transport fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "fixture-only", "base_url": upstream.URL}, Extra: map[string]any{"openai_responses_supported": true}}
		cfg := &config.Config{}
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		native := service.NewAccountTestService(codexLifetimeAccountRepo{account: account}, nil, nil, nil, nil, repository.NewHTTPUpstream(cfg), cfg, nil)
		body := []byte(`{"model":"fixture","stream":true,"input":[{"role":"user","content":"fixture"}]}`)
		_, err := native.SendCodexEvaluation(context.Background(), 918, body, nil)
		var attempted *service.HTTPUpstreamRequestError
		require.Error(t, err)
		require.False(t, errors.As(err, &attempted), "account setup must stay final")
		_, err = native.SendCodexEvaluation(context.Background(), 917, body, nil)
		require.Error(t, err)
		require.True(t, errors.As(err, &attempted), "actual transport failure needs the retry marker")
		require.True(t, attempted.RequestAttempted())
		require.True(t, errors.Is(err, attempted.Err))
	})
	for _, chat := range []bool{false, true} {
		t.Run(fmt.Sprintf("chat=%v", chat), func(t *testing.T) {
			release := make(chan struct{})
			requests := make(chan context.Context, 2)
			headers := make(chan error, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": delayed supplier\n\n")
				_ = http.NewResponseController(w).Flush()
				requests <- r.Context()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if chat {
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"21\"},\"finish_reason\":null}]}\n\n")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
				} else {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"21\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n")
				}
			}))
			t.Cleanup(upstream.Close)
			t.Cleanup(upstream.CloseClientConnections)
			account := &service.Account{ID: 917, Name: "lifetime fixture", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "fixture-only", "base_url": upstream.URL},
				Extra:       map[string]any{"openai_responses_supported": !chat}}
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			native := service.NewAccountTestService(codexLifetimeAccountRepo{account: account}, nil, nil, nil, nil,
				repository.NewHTTPUpstream(cfg), cfg, nil)
			dataDir := t.TempDir()
			codexeval.Initialize(dataDir, func(ctx context.Context, id int64, body []byte, h http.Header) (*http.Response, error) {
				response, err := native.SendCodexEvaluation(ctx, id, body, h)
				headers <- err
				return response, err
			}, native.CodexEvalAccount)
			handler := &admin.AccountHandler{}
			router := gin.New()
			admissions := make(chan context.Context, 2)
			router.POST("/start", func(c *gin.Context) { admissions <- c.Request.Context(); handler.CodexEvalStart(c) })
			router.POST("/cancel", handler.CodexEvalCancel)
			router.GET("/state", handler.CodexEvalState)
			api := httptest.NewServer(router)
			t.Cleanup(api.Close)
			post := func(path string, body any, result any) {
				t.Helper()
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				resp, err := api.Client().Post(api.URL+path, "application/json", bytes.NewReader(raw))
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.NoError(t, json.NewDecoder(resp.Body).Decode(result))
			}
			for _, stop := range []bool{false, true} {
				var response struct{ Data codexeval.StartSummary }
				post("/start", codexeval.StartRequest{Type: "candy", Targets: []codexeval.Target{{GroupID: 1, AccountID: account.ID}}, Model: "literal", Runs: 3}, &response)
				require.Len(t, response.Data.Started, 1)
				id := response.Data.Started[0].ID
				t.Cleanup(func() { codexeval.Cancel(id) })
				select {
				case err := <-headers:
					require.NoError(t, err)
				case <-time.After(3 * time.Second):
					t.Fatal("native sender did not return the streamed headers")
				}
				request := <-requests
				select {
				case <-((<-admissions).Done()):
				case <-time.After(time.Second):
					t.Fatal("HTTP start request did not finish")
				}
				poll, err := api.Client().Get(api.URL + "/state")
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, poll.Body)
				require.NoError(t, err)
				require.NoError(t, poll.Body.Close())
				require.NoError(t, request.Err(), "ending start and state requests must not cancel the supplier stream")
				if stop {
					var cancelled struct{ Data map[string]bool }
					post("/cancel", map[string]string{"run_id": id}, &cancelled)
					require.True(t, cancelled.Data["cancelled"])
					select {
					case <-request.Done():
					case <-time.After(time.Second):
						t.Fatal("explicit stop did not cancel the supplier stream")
					}
				} else {
					close(release)
				}
				require.Eventually(t, func() bool {
					raw, err := codexeval.State()
					if err != nil {
						return false
					}
					var state struct{ Active []json.RawMessage }
					return json.Unmarshal(raw, &state) == nil && len(state.Active) == 0
				}, 3*time.Second, time.Millisecond)
				raw, err := os.ReadFile(filepath.Join(dataDir, "codex-eval", "history.json"))
				require.NoError(t, err)
				var history struct {
					Results map[string][]struct {
						RunID        string `json:"run_id"`
						Status       string
						Error        string
						OK           bool
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					}
				}
				require.NoError(t, json.Unmarshal(raw, &history))
				matched := 0
				for _, record := range history.Results["917"] {
					if record.RunID != id {
						continue
					}
					matched++
					if stop {
						require.Equal(t, "cancelled", record.Status)
						require.False(t, record.OK)
						require.Contains(t, record.Error, "context canceled")
					} else {
						require.Equal(t, "completed", record.Status)
						require.True(t, record.OK)
						require.Equal(t, 4, record.InputTokens)
						require.Equal(t, 2, record.OutputTokens)
						require.Empty(t, record.Error)
					}
				}
				if stop {
					require.Equal(t, 1, matched, "stop must not start another Candy call")
				} else {
					require.Equal(t, 3, matched)
				}
				// A new blocked stream exercises explicit stop after the successful run.
				release = make(chan struct{})
				for len(requests) > 0 {
					<-requests
				}
				for len(headers) > 0 {
					<-headers
				}
			}
		})
	}
}
