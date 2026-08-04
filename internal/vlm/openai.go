package vlm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/config"
)

func init() {
	Register(TypeOpenAIVision, newOpenAIVision)
	Register(TypeGeneric, newOpenAIVision)
}

// Provider type identifiers, kept in sync with the config package by aliasing
// its constants rather than re-declaring the literals.
const (
	TypeOpenAIVision = config.TypeOpenAIVision
	TypeGeneric      = config.TypeGeneric
)

// maxErrorBody bounds how much of a downstream error page reaches the agent's
// context. Some gateways return tens of KB of HTML on failure.
const maxErrorBody = 200

// openAIVision talks to any OpenAI-compatible /chat/completions endpoint that
// accepts image_url content parts. That covers GLM-4.6V-Flash, Qwen-VL on
// DashScope, llama.cpp/llama-swap, Ollama, vLLM and OpenRouter.
type openAIVision struct {
	name         string
	baseURL      string
	apiKey       string
	model        string
	modelVersion string
	authMethod   string
	customHeader string
	headers      map[string]string
	maxTokens    int
	temperature  *float64
	client       *http.Client
}

func newOpenAIVision(name string, cfg *ProviderConfig) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	mv := cfg.ModelVersion
	if mv == "" {
		mv = cfg.Model
	}
	maxTok := cfg.MaxTokens
	if maxTok <= 0 {
		maxTok = 2048
	}
	return &openAIVision{
		name:         name,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		modelVersion: mv,
		authMethod:   cfg.AuthMethod,
		customHeader: cfg.CustomHeader,
		headers:      cfg.Headers,
		maxTokens:    maxTok,
		temperature:  cfg.Temperature,
		// A zero-value http.Client never times out; that would hang the agent.
		client: &http.Client{Timeout: cfg.Timeout()},
	}, nil
}

func (o *openAIVision) Name() string         { return o.name }
func (o *openAIVision) ModelVersion() string { return o.modelVersion }

// endpoint appends the chat-completions path unless the configured base URL
// already points at it.
func (o *openAIVision) endpoint() string {
	if strings.HasSuffix(o.baseURL, "/chat/completions") {
		return o.baseURL
	}
	return o.baseURL + "/chat/completions"
}

func (o *openAIVision) Complete(ctx context.Context, req Request) (*Response, error) {
	payload, err := o.buildPayload(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		resp, err := o.doOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (o *openAIVision) buildPayload(req Request) (map[string]any, error) {
	if len(req.Image) == 0 {
		return nil, fmt.Errorf("image data is empty")
	}
	mime := req.ImageMIME
	if mime == "" {
		mime = "image/png"
	}
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(req.Image)

	userContent := []map[string]any{
		{"type": "text", "text": req.Prompt},
		{"type": "image_url", "image_url": map[string]any{"url": dataURI}},
	}

	messages := make([]map[string]any, 0, 2)
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": userContent})

	// Three-level fallback for every optional value: request -> provider config
	// -> built-in default. Passing an empty model through is a classic 400.
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = o.maxTokens
	}
	payload := map[string]any{
		"model":      o.model,
		"messages":   messages,
		"max_tokens": maxTok,
		"stream":     false,
	}
	switch {
	case req.Temperature != nil:
		payload["temperature"] = *req.Temperature
	case o.temperature != nil:
		payload["temperature"] = *o.temperature
	case req.JSONOnly:
		// Coordinate extraction must be reproducible, so keep it near-greedy.
		payload["temperature"] = 0.1
	}
	return payload, nil
}

func (o *openAIVision) doOnce(ctx context.Context, body []byte) (*Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range o.headers {
		httpReq.Header.Set(k, v)
	}
	o.setAuth(httpReq)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, retryable{fmt.Errorf("call %s: %w", o.name, err)}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, retryable{fmt.Errorf("read %s response: %w", o.name, readErr)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := fmt.Errorf("%s HTTP %d: %s", o.name, resp.StatusCode, truncateRunes(string(raw), maxErrorBody))
		// 4xx will not fix itself on retry; 429 and 5xx might.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, retryable{e}
		}
		return nil, e
	}
	return parseChatCompletion(raw, o.name, o.model)
}

func (o *openAIVision) setAuth(req *http.Request) {
	if o.apiKey == "" {
		return // local backends need no credentials
	}
	switch {
	case o.authMethod == "bearer" || o.authMethod == "":
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	case o.authMethod == "basic":
		req.Header.Set("Authorization", "Basic "+o.apiKey)
	case o.authMethod == "custom_header" && o.customHeader != "":
		req.Header.Set(o.customHeader, o.apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
}

// chatCompletion declares only the fields we care about, so a backend adding
// new fields never breaks parsing.
type chatCompletion struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			// Content is a string for most backends but an array of parts for
			// some; json.RawMessage lets us accept both.
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func parseChatCompletion(raw []byte, providerName, fallbackModel string) (*Response, error) {
	var cc chatCompletion
	if err := json.Unmarshal(raw, &cc); err != nil {
		return nil, fmt.Errorf("%s: parse response: %w (body: %s)",
			providerName, err, truncateRunes(string(raw), maxErrorBody))
	}
	// Some gateways return HTTP 200 with an error object in the body.
	if len(cc.Error) > 0 && string(cc.Error) != "null" {
		return nil, fmt.Errorf("%s: API error: %s", providerName, truncateRunes(string(cc.Error), maxErrorBody))
	}
	if len(cc.Choices) == 0 {
		return nil, fmt.Errorf("%s: response contained no choices (body: %s)",
			providerName, truncateRunes(string(raw), maxErrorBody))
	}

	text, err := decodeContent(cc.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", providerName, err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%s: model returned empty content", providerName)
	}
	model := cc.Model
	if model == "" {
		model = fallbackModel
	}
	return &Response{
		Text:             text,
		Model:            model,
		PromptTokens:     cc.Usage.PromptTokens,
		CompletionTokens: cc.Usage.CompletionTokens,
	}, nil
}

// decodeContent accepts either a plain string or an array of content parts.
func decodeContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("message content is missing")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("unsupported content shape: %s", truncateRunes(string(raw), 120))
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

// retryable marks transient failures.
type retryable struct{ error }

func isRetryable(err error) bool {
	_, ok := err.(retryable)
	return ok
}

// truncateRunes shortens s to max runes. Rune-based so Chinese error messages
// are never cut into an invalid half-character.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "...[truncated]"
}
