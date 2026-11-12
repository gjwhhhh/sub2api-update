package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func isCodexEvalAccountSupported(account *Account) bool {
	if account == nil {
		return false
	}
	return account.IsOAuth() || account.Type == AccountTypeAPIKey || account.Type == AccountTypeUpstream
}

// Evaluation suggestions are raw upstream names. The normal account-test
// picker projects mappings and allowlists, which do not apply to evaluation.
func (s *AccountTestService) CodexEvalModels(ctx context.Context, id int64) ([]openai.Model, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isCodexEvalAccountSupported(account) {
		return nil, fmt.Errorf("unsupported account type: %s/%s", account.Platform, account.Type)
	}
	if account.Platform == PlatformOpenAI {
		discoveryAccount := account
		if account.IsOAuth() && strings.TrimSpace(account.GetCredential("base_url")) != "" {
			cloned := *account
			cloned.Type = AccountTypeAPIKey
			creds := make(map[string]any, len(account.Credentials)+1)
			for k, v := range account.Credentials {
				creds[k] = v
			}
			if _, ok := creds["api_key"]; !ok {
				creds["api_key"] = account.GetOpenAIAccessToken()
			}
			cloned.Credentials = creds
			discoveryAccount = &cloned
		}
		if s.openaiGatewayService != nil {
			catalog, err := s.openaiGatewayService.FetchOpenAIModelsList(ctx, discoveryAccount)
			if err != nil {
				return nil, err
			}
			var payload struct {
				Data []openai.Model `json:"data"`
			}
			if err := json.Unmarshal(catalog.Body, &payload); err != nil {
				return nil, err
			}
			return payload.Data, nil
		}
		if s.httpUpstream == nil {
			return nil, fmt.Errorf("OpenAI model discovery service is unavailable")
		}
		ids, err := s.FetchUpstreamSupportedModels(ctx, discoveryAccount)
		if err != nil {
			return nil, err
		}
		return modelIDsToOpenAIModels(ids, account.Platform), nil
	}
	if account.IsOpenAICompatible() && s.httpUpstream != nil && strings.TrimSpace(account.GetCredential("base_url")) != "" {
		if ids, err := s.FetchUpstreamSupportedModels(ctx, account); err == nil && len(ids) > 0 {
			return modelIDsToOpenAIModels(ids, account.Platform), nil
		}
	}

	return fallbackCodexEvalModels(account), nil
}

func modelIDsToOpenAIModels(ids []string, ownedBy string) []openai.Model {
	out := make([]openai.Model, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, openai.Model{
			ID:          id,
			Object:      "model",
			Type:        "model",
			OwnedBy:     ownedBy,
			DisplayName: id,
		})
	}
	return out
}

func fallbackCodexEvalModels(account *Account) []openai.Model {
	var ids []string
	if account != nil {
		for k, v := range account.GetModelMapping() {
			if k = strings.TrimSpace(k); k != "" && !strings.Contains(k, "*") {
				ids = append(ids, k)
			}
			if v = strings.TrimSpace(v); v != "" && !strings.Contains(v, "*") {
				ids = append(ids, v)
			}
		}
		if def := strings.TrimSpace(account.providerDefaultTestModel()); def != "" {
			ids = append(ids, def)
		}
		switch account.Platform {
		case PlatformAnthropic, PlatformTypeSafe:
			for _, m := range claude.DefaultModels {
				ids = append(ids, m.ID)
			}
		case PlatformGemini:
			for _, m := range geminicli.DefaultModels {
				ids = append(ids, m.ID)
			}
		case PlatformAntigravity:
			for _, m := range antigravity.DefaultModels() {
				ids = append(ids, m.ID)
			}
		case PlatformGrok:
			for _, m := range xai.DefaultModels() {
				ids = append(ids, m.ID)
			}
		default:
			for _, m := range openai.DefaultModels {
				ids = append(ids, m.ID)
			}
		}
	}
	platform := ""
	if account != nil {
		platform = account.Platform
	}
	return modelIDsToOpenAIModels(ids, platform)
}

func (s *AccountTestService) CodexEvalAccount(ctx context.Context, id int64) (string, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if !isCodexEvalAccountSupported(account) {
		return "", fmt.Errorf("unsupported account type: %s/%s", account.Platform, account.Type)
	}
	return account.Name, nil
}

func extractCodexSIWCAccessToken(account *Account) string {
	if account == nil {
		return ""
	}
	if tok := strings.TrimSpace(account.GetCredential("siwc_access_token")); tok != "" {
		return tok
	}
	for _, store := range []map[string]any{account.Credentials, account.Extra} {
		if store == nil {
			continue
		}
		if siwcMap, ok := store["siwc"].(map[string]any); ok {
			for _, key := range []string{"access_token", "access"} {
				if tok, ok := siwcMap[key].(string); ok && strings.TrimSpace(tok) != "" {
					return strings.TrimSpace(tok)
				}
			}
		}
	}
	return ""
}

func normalizeCodexOAuthNonLitePayload(payload map[string]json.RawMessage, model string) {
	payload["store"] = json.RawMessage("false")
	var instructions string
	if raw, ok := payload["instructions"]; ok {
		_ = json.Unmarshal(raw, &instructions)
	}
	if strings.TrimSpace(instructions) == "" {
		synth := strings.TrimSpace(defaultCodexSynthInstructions(model))
		if synth == "" {
			synth = openai.DefaultInstructions
		}
		payload["instructions"], _ = json.Marshal(synth)
	}
	rawInput, ok := payload["input"]
	if !ok {
		return
	}
	var items []map[string]any
	if err := json.Unmarshal(rawInput, &items); err != nil {
		return
	}
	changed := false
	for i, item := range items {
		if text, ok := item["content"].(string); ok {
			item["content"] = []map[string]any{
				{
					"type": "input_text",
					"text": text,
				},
			}
			items[i] = item
			changed = true
		}
	}
	if changed {
		if encoded, err := json.Marshal(items); err == nil {
			payload["input"] = encoded
		}
	}
}

// SendCodexEvaluation follows the native account-test transport. It does not
// enter the user gateway or recover operational restrictions after success.
func (s *AccountTestService) SendCodexEvaluation(ctx context.Context, id int64, body []byte, headers http.Header) (*http.Response, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}
	var model string
	var stream bool
	_ = json.Unmarshal(payload["model"], &model)
	_ = json.Unmarshal(payload["stream"], &stream)
	if strings.TrimSpace(model) == "" || !stream {
		return nil, fmt.Errorf("model and stream:true are required")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isCodexEvalAccountSupported(account) {
		return nil, fmt.Errorf("unsupported account type: %s/%s", account.Platform, account.Type)
	}
	credentialAccount := account
	if account.IsCredentialShadow() {
		credentialAccount, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
	}

	if credentialAccount.Platform == PlatformAnthropic || (credentialAccount.RoutesProtocolByInbound() && credentialAccount.GetAPIProtocol() == APIProtocolAnthropic) || (credentialAccount.routesByModel() && credentialAccount.resolveModelRoutedProtocol(model) == APIProtocolAnthropic) {
		return s.sendCodexAnthropicEvaluation(ctx, account, credentialAccount, model, body)
	}
	switch credentialAccount.Platform {
	case PlatformGemini, PlatformAntigravity:
		return s.sendCodexGeminiEvaluation(ctx, account, credentialAccount, model, body)
	}

	isSIWCProbe := strings.HasPrefix(strings.TrimSpace(headers.Get("User-Agent")), "cpa-siwc-check/")
	siwcToken := ""
	if isSIWCProbe {
		siwcToken = extractCodexSIWCAccessToken(credentialAccount)
	}
	isOAuth := credentialAccount.IsOAuth() && siwcToken == ""
	apiURL := chatgptCodexAPIURL
	authToken := ""
	chat := false
	if isOAuth {
		if !credentialAccount.IsOpenAIAgentIdentity() {
			authToken = credentialAccount.GetOpenAIAccessToken()
			if authToken == "" {
				return nil, fmt.Errorf("no access token available")
			}
		}
		if customBase := strings.TrimSpace(credentialAccount.GetCredential("base_url")); customBase != "" {
			normalized, validationErr := s.validateUpstreamBaseURL(customBase)
			if validationErr != nil {
				return nil, fmt.Errorf("invalid base URL: %w", validationErr)
			}
			apiURL = buildOpenAIResponsesURLForPlatform(credentialAccount.Platform, normalized)
		}
		payload["store"] = json.RawMessage("false")
		if headers.Get("X-Openai-Internal-Codex-Responses-Lite") != "true" {
			model = normalizeOpenAIModelForUpstream(credentialAccount, model)
			normalizeCodexOAuthNonLitePayload(payload, model)
		}
	} else {
		authToken = siwcToken
		if authToken == "" {
			authToken = credentialAccount.GetOpenAIProtocolAPIKey()
		}
		if authToken == "" {
			authToken = strings.TrimSpace(credentialAccount.GetCredential("api_key"))
		}
		if authToken == "" {
			return nil, fmt.Errorf("no API key available")
		}
		baseURL := strings.TrimSpace(credentialAccount.GetCredential("base_url"))
		if baseURL == "" {
			baseURL = credentialAccount.GetOpenAIBaseURL()
		}
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalized, validationErr := s.validateUpstreamBaseURL(baseURL)
		if validationErr != nil {
			return nil, fmt.Errorf("invalid base URL: %w", validationErr)
		}
		if siwcToken == "" {
			if credentialAccount.RoutesProtocolByInbound() {
				proto := credentialAccount.GetAPIProtocol()
				chat = proto != APIProtocolResponses && (proto != APIProtocolAdaptive || !credentialAccount.SupportsNativeCNResponses())
			} else if credentialAccount.routesByModel() {
				chat = credentialAccount.resolveModelRoutedProtocol(model) != APIProtocolResponses
			} else {
				chat = !openai_compat.ShouldUseResponsesAPI(account.Extra)
			}
		}
		if chat {
			apiURL = buildOpenAIChatCompletionsURL(normalized)
		} else {
			apiURL = buildOpenAIResponsesURLForPlatform(credentialAccount.Platform, normalized)
		}
	}
	payload["model"], _ = json.Marshal(model)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var responseRequest apicompat.ResponsesRequest
	if chat {
		if err = json.Unmarshal(encoded, &responseRequest); err != nil {
			return nil, err
		}
		converted, convertErr := apicompat.ResponsesToChatCompletionsRequest(&responseRequest)
		if convertErr != nil {
			return nil, convertErr
		}
		converted.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
		encoded, err = json.Marshal(converted)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	if !isOAuth && !chat && siwcToken == "" {
		applyOpenAICodexProbeHeaders(req.Header)
	}
	// Only Codex protocol fields are accepted from the caller. Administrator
	// authorization, target URL and proxy never leave this boundary.
	for _, key := range []string{"Accept", "Originator", "User-Agent", "Session-Id", "Thread-Id", "X-Client-Request-Id", "X-Codex-Window-Id", "X-Codex-Turn-Metadata", "X-Codex-Beta-Features", "X-Openai-Internal-Codex-Responses-Lite"} {
		if values := headers.Values(key); len(values) > 0 {
			req.Header[key] = append([]string(nil), values...)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if credentialAccount.IsOpenAIAgentIdentity() && siwcToken == "" {
		authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount)
		if authErr != nil {
			return nil, authErr
		}
		for key, values := range authHeaders {
			req.Header[key] = values
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	if isOAuth {
		req.Host = "chatgpt.com"
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		canonical := resolveCodexOutboundIdentity(req.Header.Get("User-Agent"))
		req.Header.Set("Originator", canonical.originator)
		if custom := strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()); custom != "" {
			req.Header.Set("User-Agent", custom)
		} else if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", canonical.userAgent)
		}
		setOpenAIChatGPTAccountHeaders(req.Header, credentialAccount)
		enforceCodexIdentityHeadersWithUA(req.Header, credentialAccount.GetOpenAIUserAgent())
	}
	if chat {
		applyOpenCodeUpstreamUserAgent(account, apiURL, req.Header)
	}
	credentialAccount.ApplyHeaderOverrides(req.Header)
	if chat {
		applyOpenCodeSessionHeader(nil, account, apiURL, req.Header, body, encoded)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	if err != nil {
		return nil, err
	}
	if isOAuth && s.accountRepo != nil {
		if updates, updateErr := extractOpenAICodexProbeUpdates(resp); updateErr == nil && len(updates) > 0 {
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, updates)
			mergeAccountExtra(account, updates)
		}
	}
	if resp.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		raw = redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, credentialAccount, raw)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, account, resp.Header, raw)
		}
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			_ = s.accountRepo.SetError(ctx, account.ID, fmt.Sprintf("Authentication failed (401): %s", raw))
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		return resp, readErr
	}
	if chat {
		state := apicompat.NewChatCompletionsToResponsesStreamState(model)
		tools, toolsErr := apicompat.EffectiveResponsesTools(&responseRequest)
		if toolsErr != nil {
			_ = resp.Body.Close()
			return nil, toolsErr
		}
		state.CustomTools = apicompat.CustomToolNames(tools)
		state.FunctionTools = apicompat.FunctionToolNames(tools)
		state.ToolSearchDeclared = apicompat.HasToolSearchTool(tools)
		state.NamespaceTools = apicompat.NamespaceToolNames(tools)
		resp.Body = &codexChatStream{body: resp.Body, reader: bufio.NewReader(resp.Body), state: state}
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
	}
	return resp, nil
}

func stripCodexAdditionalToolsFromResponsesBody(body []byte) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if items, ok := raw["input"].([]any); ok {
		filtered := make([]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "additional_tools" {
					continue
				}
			}
			filtered = append(filtered, item)
		}
		raw["input"] = filtered
	}
	delete(raw, "tools")
	delete(raw, "tool_choice")
	encoded, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return encoded
}

func buildAnthropicMessagesEvalURL(normalizedBaseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(normalizedBaseURL), "/")
	if strings.HasSuffix(trimmed, "/v1/messages") {
		return trimmed + "?beta=true"
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/messages?beta=true"
	}
	return trimmed + "/v1/messages?beta=true"
}

func (s *AccountTestService) sendCodexAnthropicEvaluation(ctx context.Context, account, credentialAccount *Account, model string, body []byte) (*http.Response, error) {
	cleanedBody := stripCodexAdditionalToolsFromResponsesBody(body)
	var responseRequest apicompat.ResponsesRequest
	if err := json.Unmarshal(cleanedBody, &responseRequest); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}
	responseRequest.Model = model
	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&responseRequest)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}
	anthropicReq.Model = model
	anthropicReq.Stream = true
	if anthropicReq.MaxTokens <= 0 {
		anthropicReq.MaxTokens = 16384
	}
	encoded, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, err
	}

	var apiURL string
	var authToken string
	isOAuth := credentialAccount.IsOAuth()
	baseURL := strings.TrimSpace(credentialAccount.GetBaseURL())
	if isOAuth {
		authToken = strings.TrimSpace(credentialAccount.GetCredential("access_token"))
		if authToken == "" && s.claudeTokenProvider != nil {
			if token, tokenErr := s.claudeTokenProvider.GetAccessToken(ctx, credentialAccount); tokenErr == nil {
				authToken = strings.TrimSpace(token)
			}
		}
		if authToken == "" {
			return nil, fmt.Errorf("no access token available")
		}
		if baseURL != "" {
			normalized, validationErr := s.validateUpstreamBaseURL(baseURL)
			if validationErr != nil {
				return nil, fmt.Errorf("invalid base URL: %w", validationErr)
			}
			apiURL = buildAnthropicMessagesEvalURL(normalized)
		} else {
			apiURL = testClaudeAPIURL
		}
	} else {
		authToken = strings.TrimSpace(credentialAccount.GetCredential("api_key"))
		if authToken == "" {
			authToken = strings.TrimSpace(credentialAccount.GetOpenAIProtocolAPIKey())
		}
		if authToken == "" {
			return nil, fmt.Errorf("no API key available")
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(credentialAccount.GetAnthropicProtocolBaseURL())
		}
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		normalized, validationErr := s.validateUpstreamBaseURL(baseURL)
		if validationErr != nil {
			return nil, fmt.Errorf("invalid base URL: %w", validationErr)
		}
		apiURL = buildAnthropicMessagesEvalURL(normalized)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	for k, v := range claude.DefaultHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	if isOAuth {
		req.Header.Set("anthropic-beta", claude.DefaultBetaHeader)
		req.Header.Set("Authorization", "Bearer "+authToken)
	} else {
		req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
		setAnthropicAPIKeyAuthHeader(req.Header, credentialAccount, authToken, baseURL)
	}
	credentialAccount.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.doUpstreamModelsRequest(req, proxyURL, account)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		return resp, readErr
	}
	state := apicompat.NewAnthropicEventToResponsesState()
	state.Model = model
	resp.Body = &codexAnthropicStream{body: resp.Body, reader: bufio.NewReader(resp.Body), state: state, model: model}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return resp, nil
}

func (s *AccountTestService) sendCodexGeminiEvaluation(ctx context.Context, account, credentialAccount *Account, model string, body []byte) (*http.Response, error) {
	cleanedBody := stripCodexAdditionalToolsFromResponsesBody(body)
	var responseRequest apicompat.ResponsesRequest
	if err := json.Unmarshal(cleanedBody, &responseRequest); err != nil {
		return nil, fmt.Errorf("invalid Responses request: %w", err)
	}
	responseRequest.Model = model
	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&responseRequest)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}
	anthropicReq.Model = model
	anthropicReq.Stream = true
	if anthropicReq.MaxTokens <= 0 {
		anthropicReq.MaxTokens = 16384
	}
	claudeBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, err
	}
	geminiPayload, err := convertClaudeMessagesToGeminiGenerateContent(claudeBody, model)
	if err != nil {
		return nil, fmt.Errorf("convert to gemini request: %w", err)
	}
	geminiPayload = applyGeminiCompatThinkingConfig(geminiPayload, claudeBody, model, true)

	var req *http.Request
	if credentialAccount.IsOAuth() {
		accessToken := strings.TrimSpace(credentialAccount.GetCredential("access_token"))
		if accessToken == "" {
			if credentialAccount.Platform == PlatformGemini && s.geminiTokenProvider != nil {
				if token, tokenErr := s.geminiTokenProvider.GetAccessToken(ctx, credentialAccount); tokenErr == nil {
					accessToken = strings.TrimSpace(token)
				}
			} else if credentialAccount.Platform == PlatformAntigravity && s.antigravityGatewayService != nil && s.antigravityGatewayService.GetTokenProvider() != nil {
				if token, tokenErr := s.antigravityGatewayService.GetTokenProvider().GetAccessToken(ctx, credentialAccount); tokenErr == nil {
					accessToken = strings.TrimSpace(token)
				}
			}
		}
		if accessToken == "" {
			return nil, fmt.Errorf("no access token available")
		}
		baseURL := strings.TrimSpace(credentialAccount.GetCredential("base_url"))
		projectID := strings.TrimSpace(credentialAccount.GetCredential("project_id"))
		if projectID != "" && baseURL == "" {
			req, err = s.buildCodeAssistRequest(ctx, accessToken, projectID, model, geminiPayload)
			if err != nil {
				return nil, err
			}
		} else {
			if baseURL == "" {
				baseURL = geminicli.AIStudioBaseURL
			}
			normalizedBaseURL, validationErr := s.validateUpstreamBaseURL(baseURL)
			if validationErr != nil {
				return nil, fmt.Errorf("invalid base URL: %w", validationErr)
			}
			fullURL, urlErr := buildGeminiAIStudioModelActionURL(normalizedBaseURL, model, "streamGenerateContent", true)
			if urlErr != nil {
				return nil, urlErr
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(geminiPayload))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}
	} else {
		apiKey := strings.TrimSpace(credentialAccount.GetCredential("api_key"))
		if apiKey == "" {
			return nil, fmt.Errorf("no API key available")
		}
		baseURL := strings.TrimSpace(credentialAccount.GetCredential("base_url"))
		if baseURL == "" {
			baseURL = geminicli.AIStudioBaseURL
		}
		normalizedBaseURL, validationErr := s.validateUpstreamBaseURL(baseURL)
		if validationErr != nil {
			return nil, fmt.Errorf("invalid base URL: %w", validationErr)
		}
		fullURL, urlErr := buildGeminiAIStudioModelActionURL(normalizedBaseURL, model, "streamGenerateContent", true)
		if urlErr != nil {
			return nil, urlErr
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(geminiPayload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("x-goog-api-key", apiKey)
	}
	credentialAccount.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.doUpstreamModelsRequest(req, proxyURL, account)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		return resp, readErr
	}
	resp.Body = &codexGeminiStream{body: resp.Body, reader: bufio.NewReader(resp.Body), model: model}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return resp, nil
}

// Pull conversion uses the native bridge without a producer goroutine or pipe.
type codexChatStream struct {
	body     io.ReadCloser
	reader   *bufio.Reader
	state    *apicompat.ChatCompletionsToResponsesStreamState
	pending  []byte
	finished bool
}

func (s *codexChatStream) Close() error { return s.body.Close() }
func (s *codexChatStream) fail(err error) {
	raw, _ := json.Marshal(map[string]any{"type": "error", "message": err.Error(), "usage": s.state.Usage})
	s.pending = append([]byte("event: error\ndata: "), raw...)
	s.pending = append(s.pending, '\n', '\n')
	s.finished = true
}
func (s *codexChatStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return 0, io.EOF
		}
		data, err := codexeval.SSEData(s.reader)
		if err != nil && err != io.EOF {
			s.fail(err)
			continue
		}
		var events []apicompat.ResponsesStreamEvent
		if err == io.EOF || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if s.state.FinishReason == "" {
				s.fail(fmt.Errorf("chat completions stream ended before finish_reason"))
				continue
			}
			if err = s.state.ValidateToolCallArguments(); err != nil {
				s.fail(err)
				continue
			}
			events = apicompat.FinalizeChatCompletionsResponsesStream(s.state)
			s.finished = true
		} else {
			var upstreamError struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(data, &upstreamError)
			if len(upstreamError.Error) > 0 && string(upstreamError.Error) != "null" {
				s.fail(fmt.Errorf("chat completions error: %s", data))
				continue
			}
			var chunk apicompat.ChatCompletionsChunk
			if err = json.Unmarshal(data, &chunk); err != nil {
				continue
			}
			events = apicompat.ChatCompletionsChunkToResponsesEvents(&chunk, s.state)
		}
		for _, event := range events {
			raw, encodeErr := json.Marshal(event)
			if encodeErr != nil {
				return 0, encodeErr
			}
			s.pending = append(s.pending, []byte("event: "+event.Type+"\ndata: ")...)
			s.pending = append(s.pending, raw...)
			s.pending = append(s.pending, '\n', '\n')
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

type codexAnthropicStream struct {
	body     io.ReadCloser
	reader   *bufio.Reader
	state    *apicompat.AnthropicEventToResponsesState
	model    string
	pending  []byte
	finished bool
}

func (s *codexAnthropicStream) Close() error { return s.body.Close() }
func (s *codexAnthropicStream) fail(err error) {
	raw, _ := json.Marshal(map[string]any{"type": "error", "message": err.Error()})
	s.pending = append([]byte("event: error\ndata: "), raw...)
	s.pending = append(s.pending, '\n', '\n')
	s.finished = true
}
func (s *codexAnthropicStream) flushOpenBlock() []apicompat.ResponsesStreamEvent {
	if s.state.CurrentItemType == "message" && s.state.TextAccum != "" {
		return apicompat.AnthropicEventToResponsesEvents(&apicompat.AnthropicStreamEvent{Type: "content_block_stop"}, s.state)
	}
	if s.state.CurrentItemType == "reasoning" {
		return apicompat.AnthropicEventToResponsesEvents(&apicompat.AnthropicStreamEvent{Type: "content_block_stop"}, s.state)
	}
	return nil
}
func (s *codexAnthropicStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return 0, io.EOF
		}
		data, err := codexeval.SSEData(s.reader)
		if err != nil && err != io.EOF {
			s.fail(err)
			continue
		}
		var events []apicompat.ResponsesStreamEvent
		if err == io.EOF || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if !s.state.CreatedSent {
				s.fail(fmt.Errorf("anthropic stream ended before message_start"))
				continue
			}
			events = append(events, s.flushOpenBlock()...)
			events = append(events, apicompat.FinalizeAnthropicResponsesStream(s.state)...)
			s.finished = true
		} else {
			var evt apicompat.AnthropicStreamEvent
			if err = json.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Type == "error" {
				s.fail(fmt.Errorf("anthropic error: %s", data))
				continue
			}
			if !s.state.CreatedSent && evt.Type != "message_start" {
				events = append(events, apicompat.AnthropicEventToResponsesEvents(&apicompat.AnthropicStreamEvent{
					Type:    "message_start",
					Message: &apicompat.AnthropicResponse{ID: "msg_eval", Type: "message", Role: "assistant", Model: s.model},
				}, s.state)...)
			}
			if evt.Type == "content_block_delta" && evt.Delta != nil {
				if evt.Delta.Type == "text_delta" && s.state.CurrentItemType != "message" {
					idx := 0
					events = append(events, apicompat.AnthropicEventToResponsesEvents(&apicompat.AnthropicStreamEvent{
						Type:         "content_block_start",
						Index:        &idx,
						ContentBlock: &apicompat.AnthropicContentBlock{Type: "text"},
					}, s.state)...)
				} else if evt.Delta.Type == "thinking_delta" && s.state.CurrentItemType != "reasoning" {
					idx := 0
					events = append(events, apicompat.AnthropicEventToResponsesEvents(&apicompat.AnthropicStreamEvent{
						Type:         "content_block_start",
						Index:        &idx,
						ContentBlock: &apicompat.AnthropicContentBlock{Type: "thinking"},
					}, s.state)...)
				}
			}
			if evt.Type == "message_stop" {
				events = append(events, s.flushOpenBlock()...)
			}
			events = append(events, apicompat.AnthropicEventToResponsesEvents(&evt, s.state)...)
			if s.state.CompletedSent {
				s.finished = true
			}
		}
		for _, event := range events {
			raw, encodeErr := json.Marshal(event)
			if encodeErr != nil {
				return 0, encodeErr
			}
			s.pending = append(s.pending, []byte("event: "+event.Type+"\ndata: ")...)
			s.pending = append(s.pending, raw...)
			s.pending = append(s.pending, '\n', '\n')
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

type codexGeminiStream struct {
	body            io.ReadCloser
	reader          *bufio.Reader
	model           string
	pending         []byte
	finished        bool
	createdSent     bool
	seenCandidate   bool
	seenText        geminiTextDelta
	seenThought     geminiTextDelta
	textBuilder     strings.Builder
	inputTokens     int64
	outputTokens    int64
	reasoningTokens int64
}

func (s *codexGeminiStream) Close() error { return s.body.Close() }
func (s *codexGeminiStream) fail(err error) {
	raw, _ := json.Marshal(map[string]any{"type": "error", "message": err.Error()})
	s.pending = append([]byte("event: error\ndata: "), raw...)
	s.pending = append(s.pending, '\n', '\n')
	s.finished = true
}
func (s *codexGeminiStream) emit(eventType string, payload map[string]any) {
	payload["type"] = eventType
	raw, _ := json.Marshal(payload)
	s.pending = append(s.pending, []byte("event: "+eventType+"\ndata: ")...)
	s.pending = append(s.pending, raw...)
	s.pending = append(s.pending, '\n', '\n')
}
func (s *codexGeminiStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		if s.finished {
			return 0, io.EOF
		}
		data, err := codexeval.SSEData(s.reader)
		if err != nil && err != io.EOF {
			s.fail(err)
			continue
		}
		if err == io.EOF || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			if !s.seenCandidate {
				s.fail(fmt.Errorf("gemini stream ended before candidates"))
				continue
			}
			msgItem := map[string]any{
				"type":   "message",
				"id":     "msg_gemini",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{
					{"type": "output_text", "text": s.textBuilder.String()},
				},
			}
			s.emit("response.output_item.done", map[string]any{"item": msgItem})
			s.emit("response.completed", map[string]any{
				"response": map[string]any{
					"id":     "resp_gemini",
					"object": "response",
					"model":  s.model,
					"status": "completed",
					"output": []map[string]any{msgItem},
					"usage": map[string]any{
						"input_tokens":  s.inputTokens,
						"output_tokens": s.outputTokens,
						"output_tokens_details": map[string]any{
							"reasoning_tokens": s.reasoningTokens,
						},
					},
				},
			})
			s.finished = true
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if errObj, ok := chunk["error"].(map[string]any); ok && errObj != nil {
			s.fail(fmt.Errorf("gemini error: %s", data))
			continue
		}
		if inner, ok := chunk["response"].(map[string]any); ok && inner != nil {
			chunk = inner
		}
		if um, ok := chunk["usageMetadata"].(map[string]any); ok && um != nil {
			if v := codexEvalInt64(um["promptTokenCount"]); v > 0 {
				s.inputTokens = v
			}
			cand := codexEvalInt64(um["candidatesTokenCount"])
			thought := codexEvalInt64(um["thoughtsTokenCount"])
			if cand+thought > 0 {
				s.outputTokens = cand + thought
			}
			if thought > 0 {
				s.reasoningTokens = thought
			}
		}
		if candidates, ok := chunk["candidates"].([]any); ok && len(candidates) > 0 {
			s.seenCandidate = true
			if !s.createdSent {
				s.createdSent = true
				s.emit("response.created", map[string]any{
					"response": map[string]any{"id": "resp_gemini", "object": "response", "model": s.model, "status": "in_progress"},
				})
			}
			for _, part := range extractGeminiParts(chunk) {
				text, _ := part["text"].(string)
				if text == "" {
					continue
				}
				if isGeminiThoughtPart(part) {
					delta := s.seenThought.next(text)
					if delta != "" {
						s.emit("response.reasoning_summary_text.delta", map[string]any{"delta": delta})
					}
				} else {
					delta := s.seenText.next(text)
					if delta != "" {
						_, _ = s.textBuilder.WriteString(delta)
						s.emit("response.output_text.delta", map[string]any{"delta": delta})
					}
				}
			}
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func codexEvalInt64(v any) int64 {
	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case int:
		return int64(val)
	case json.Number:
		i, _ := val.Int64()
		return i
	default:
		return 0
	}
}
