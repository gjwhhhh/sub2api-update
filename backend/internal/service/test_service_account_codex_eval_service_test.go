//go:build unit

// sub2api-package: backend/internal/service
package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexEvalNativeSender(t *testing.T) {
	for _, chat := range []bool{false, true} {
		name := "responses"
		if chat {
			name = "chat-completions"
		}
		t.Run(name, func(t *testing.T) {
			account := &Account{ID: 17, Name: "selected", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-upstream-key", "base_url": "https://api.openai.com", "model_mapping": map[string]any{"requested": "mapped"}}, Extra: map[string]any{"openai_responses_supported": !chat}}
			repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{17: account}}}
			events := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"21"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}` + "\n\n"
			if chat {
				events = "data: {\"id\":\"fixture\",\"model\":\"requested\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"21\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\ndata: [DONE]\n\n"
			}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, events)}}
			cfg := &config.Config{}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: cfg}
			body := []byte(`{"model":"requested","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"complete prompt"}]}],"tools":[{"type":"function","name":"fixture_tool","parameters":{"type":"object"}}],"reasoning":{"effort":"none"},"client_metadata":{"turn_id":"fixture"}}`)
			resp, err := svc.SendCodexEvaluation(context.Background(), 17, body, http.Header{"Authorization": {"Bearer administrator-session"}, "Session-Id": {"fixture-session"}})
			require.NoError(t, err)
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(raw), "response.completed")
			require.Contains(t, string(raw), "21")
			require.Contains(t, string(raw), "input_tokens\":9")
			require.Len(t, upstream.requests, 1)
			req := upstream.requests[0]
			sent, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "Bearer fixture-upstream-key", req.Header.Get("Authorization"))
			require.Equal(t, "fixture-session", req.Header.Get("Session-Id"))
			require.Equal(t, "requested", gjson.GetBytes(sent, "model").String())
			require.Contains(t, string(sent), "complete prompt")
			require.Contains(t, string(sent), "fixture_tool")
			if chat {
				require.True(t, strings.HasSuffix(req.URL.Path, "/chat/completions"))
				require.Equal(t, "none", gjson.GetBytes(sent, "reasoning_effort").String())
			} else {
				require.True(t, strings.HasSuffix(req.URL.Path, "/responses"))
				require.Equal(t, "fixture", gjson.GetBytes(sent, "client_metadata.turn_id").String())
			}
			require.Zero(t, repo.clearedErrorID)
			_, err = svc.SendCodexEvaluation(context.Background(), 18, body, nil)
			require.ErrorContains(t, err, "account not found")
			require.Len(t, upstream.requests, 1)
			upstream.responses = []*http.Response{newJSONResponse(401, `{"error":{"message":"fixture authentication failure"}}`)}
			resp, err = svc.SendCodexEvaluation(context.Background(), 17, body, nil)
			require.NoError(t, err)
			raw, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			require.Equal(t, 401, resp.StatusCode)
			require.Contains(t, string(raw), "fixture authentication failure")
			require.Equal(t, int64(17), repo.setErrorID)
			for _, status := range []int{401, 429} {
				upstream.responses = []*http.Response{{StatusCode: status, Header: http.Header{}, Body: codexErrorBody{}}}
				resp, err = svc.SendCodexEvaluation(context.Background(), 17, body, nil)
				require.ErrorContains(t, err, "fixture body read")
				require.NotNil(t, resp)
				require.Equal(t, status, resp.StatusCode)
				resp.Body.Close()
			}
		})
	}
}

type codexErrorBody struct{}

func (codexErrorBody) Read([]byte) (int, error) { return 0, errors.New("fixture body read") }
func (codexErrorBody) Close() error             { return nil }

func TestCodexEvalModelsBypassesAccountProjection(t *testing.T) {
	gateway := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(`{"data":[{"id":"upstream-target"},{"id":"unconfigured-model"}]}`), nil
	}})
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.ID = 17
	account.Credentials["model_mapping"] = map[string]any{"configured-alias": "upstream-target"}
	repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{17: account}}}
	svc := &AccountTestService{accountRepo: repo, openaiGatewayService: gateway}
	models, err := svc.CodexEvalModels(context.Background(), 17)
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "upstream-target", models[0].ID)
	require.Equal(t, "unconfigured-model", models[1].ID)
	ordinary, err := svc.FetchOpenAIAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Len(t, ordinary, 1)
	require.Equal(t, "configured-alias", ordinary[0].ID)
}

func TestCodexEvalChatStreamFailures(t *testing.T) {
	for _, failure := range []struct {
		wire, message string
	}{
		{`data: {"error":{"message":"actual upstream failure"}}` + "\n\n", "actual upstream failure"},
		{"", "stream ended before finish_reason"},
	} {
		t.Run(failure.message, func(t *testing.T) {
			wire := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n" + failure.wire
			body := io.NopCloser(strings.NewReader(wire))
			stream := &codexChatStream{body: body, reader: bufio.NewReader(body), state: apicompat.NewChatCompletionsToResponsesStreamState("fixture")}
			raw, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"type":"error"`)
			require.Contains(t, string(raw), failure.message)
			require.Contains(t, string(raw), `"input_tokens":9`)
			require.NotContains(t, string(raw), "response.completed")
		})
	}
}

func TestCodexEvalOAuthModelNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, lite, wantModel string
	}{
		{name: "native-codex", wantModel: "gpt-5.4"},
		{name: "responses-lite", lite: "true", wantModel: "gpt-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID: 53, Name: "codex-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "fixture-oauth-token", "model_mapping": map[string]any{"gpt-5": "configured-model"}},
			}
			repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{53: account}}}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, "")}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
			body := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"Reply with OK."}]}`)
			headers := http.Header{}
			if tc.lite != "" {
				headers.Set("X-Openai-Internal-Codex-Responses-Lite", tc.lite)
			}
			resp, err := svc.SendCodexEvaluation(context.Background(), 53, body, headers)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Len(t, upstream.requests, 1)
			req := upstream.requests[0]
			require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", req.URL.String())
			require.Equal(t, "Bearer fixture-oauth-token", req.Header.Get("Authorization"))
			sent, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantModel, gjson.GetBytes(sent, "model").String())
			require.False(t, gjson.GetBytes(sent, "store").Bool())
			if tc.lite == "true" {
				require.Equal(t, "true", req.Header.Get("X-Openai-Internal-Codex-Responses-Lite"))
				require.False(t, gjson.GetBytes(sent, "instructions").Exists())
				require.Equal(t, "Reply with OK.", gjson.GetBytes(sent, "input.0.content").String())
			} else {
				require.NotEmpty(t, gjson.GetBytes(sent, "instructions").String())
				require.Equal(t, "input_text", gjson.GetBytes(sent, "input.0.content.0.type").String())
			}
		})
	}
}

func TestCodexEvalMultiProviderSendersAndSIWC(t *testing.T) {
	body := []byte(`{"model":"requested-model","stream":true,"instructions":"system prompt","input":[{"role":"user","content":[{"type":"input_text","text":"solve 21"}]}],"reasoning":{"effort":"low"}}`)

	t.Run("anthropic-apikey", func(t *testing.T) {
		account := &Account{
			ID: 31, Name: "claude-key", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-ant-fixture", "base_url": "https://api.anthropic.com", "model_mapping": map[string]any{"claude-3-7-sonnet": "claude-3-7-sonnet-20250219"}},
		}
		repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{31: account}}}
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"let me think\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"21\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, sse)}}
		svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
		models, err := svc.CodexEvalModels(context.Background(), 31)
		require.NoError(t, err)
		require.NotEmpty(t, models)
		resp, err := svc.SendCodexEvaluation(context.Background(), 31, body, nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Contains(t, string(raw), "response.reasoning_summary_text.delta")
		require.Contains(t, string(raw), "response.output_text.delta")
		require.Contains(t, string(raw), "response.completed")
		require.Contains(t, string(raw), `"input_tokens":12`)
		require.Len(t, upstream.requests, 1)
		require.Equal(t, "sk-ant-fixture", upstream.requests[0].Header.Get("x-api-key"))
		require.True(t, strings.HasSuffix(upstream.requests[0].URL.Path, "/v1/messages"))
	})

	t.Run("gemini-and-antigravity", func(t *testing.T) {
		for _, platform := range []string{PlatformGemini, PlatformAntigravity} {
			account := &Account{
				ID: 41, Name: "gemini-key", Platform: platform, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "AIza-fixture", "base_url": "https://generativelanguage.googleapis.com"},
			}
			repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{41: account}}}
			sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking...\",\"thought\":true},{\"text\":\"21\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":2}}\n\n"
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, sse)}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
			models, err := svc.CodexEvalModels(context.Background(), 41)
			require.NoError(t, err)
			require.NotEmpty(t, models)
			resp, err := svc.SendCodexEvaluation(context.Background(), 41, body, nil)
			require.NoError(t, err)
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.NoError(t, err)
			require.Contains(t, string(raw), "response.completed")
			require.Contains(t, string(raw), "21")
			require.Contains(t, string(raw), `"input_tokens":10`)
			require.Contains(t, string(raw), `"reasoning_tokens":2`)
		}
	})

	t.Run("deepseek-and-grok-platforms", func(t *testing.T) {
		account := &Account{
			ID: 61, Name: "deepseek-key", Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "ds-fixture-key", "base_url": "https://api.deepseek.com", "model_mapping": map[string]any{"deepseek-chat": "deepseek-chat"}},
		}
		repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{61: account}}}
		sse := "data: {\"id\":\"ds-1\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"21\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"ds-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":1,\"total_tokens\":7}}\n\ndata: [DONE]\n\n"
		upstream := &queuedHTTPUpstream{responses: []*http.Response{
			ordinaryModelsUpstreamResponse(`{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`),
			newJSONResponse(200, sse),
		}}
		svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
		name, err := svc.CodexEvalAccount(context.Background(), 61)
		require.NoError(t, err)
		require.Equal(t, "deepseek-key", name)
		models, err := svc.CodexEvalModels(context.Background(), 61)
		require.NoError(t, err)
		require.NotEmpty(t, models)
		resp, err := svc.SendCodexEvaluation(context.Background(), 61, body, nil)
		require.NoError(t, err)
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Contains(t, string(raw), "response.completed")
		require.Contains(t, string(raw), "21")
	})

	t.Run("siwc-probe-oauth-and-siwc-token", func(t *testing.T) {
		siwcBody := []byte(`{"model":"gpt-5","store":false,"stream":true,"input":[{"role":"user","content":"Reply with OK."}]}`)
		siwcHeaders := http.Header{"User-Agent": []string{"cpa-siwc-check/0.4.6"}}

		oauthAccount := &Account{
			ID: 51, Name: "codex-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{"access_token": "oauth-token-123"},
		}
		repo := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{51: oauthAccount}}}
		sse := `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n"
		upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, sse)}}
		svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
		resp, err := svc.SendCodexEvaluation(context.Background(), 51, siwcBody, siwcHeaders)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Len(t, upstream.requests, 1)
		require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.requests[0].URL.String())
		require.Equal(t, "Bearer oauth-token-123", upstream.requests[0].Header.Get("Authorization"))
		sentPayload, _ := io.ReadAll(upstream.requests[0].Body)
		require.Equal(t, "gpt-5.4", gjson.GetBytes(sentPayload, "model").String())
		require.Contains(t, string(sentPayload), `"instructions":`)
		require.Contains(t, string(sentPayload), `"type":"input_text"`)

		siwcBoundAccount := &Account{
			ID: 52, Name: "codex-siwc-bound", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "oauth-token-123",
				"siwc":         map[string]any{"access_token": "siwc-token-xyz"},
			},
		}
		repo2 := &openAIAccountTestRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{52: siwcBoundAccount}}}
		upstream2 := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(200, sse)}}
		svc2 := &AccountTestService{accountRepo: repo2, httpUpstream: upstream2, cfg: &config.Config{}}
		resp2, err := svc2.SendCodexEvaluation(context.Background(), 52, siwcBody, siwcHeaders)
		require.NoError(t, err)
		_ = resp2.Body.Close()
		require.Len(t, upstream2.requests, 1)
		require.Equal(t, "https://api.openai.com/v1/responses", upstream2.requests[0].URL.String())
		require.Equal(t, "Bearer siwc-token-xyz", upstream2.requests[0].Header.Get("Authorization"))
		require.Equal(t, "cpa-siwc-check/0.4.6", upstream2.requests[0].Header.Get("User-Agent"))
		boundPayload, err := io.ReadAll(upstream2.requests[0].Body)
		require.NoError(t, err)
		require.Equal(t, "gpt-5", gjson.GetBytes(boundPayload, "model").String())
		require.False(t, gjson.GetBytes(boundPayload, "instructions").Exists())
		require.Equal(t, "Reply with OK.", gjson.GetBytes(boundPayload, "input.0.content").String())
	})
}
