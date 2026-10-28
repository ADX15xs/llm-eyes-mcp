package vlm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/config"
)

// openai_test covers the OpenAI-compatible adapter: endpoint construction,
// payload assembly, auth, tolerant response parsing, and retry behaviour.

func newTestOA(t *testing.T, model string) *openAIVision {
	t.Helper()
	p, err := newOpenAIVision("test", &config.ProviderConfig{
		Type:    TypeOpenAIVision,
		BaseURL: "http://example/v1",
		Model:   model,
		APIKey:  "secret",
	})
	if err != nil {
		t.Fatalf("newOpenAIVision: %v", err)
	}
	return p.(*openAIVision)
}

func TestEndpointAppendsPathWhenMissing(t *testing.T) {
	p := newTestOA(t, "m")
	p.baseURL = "http://example/v1"
	if got := p.endpoint(); got != "http://example/v1/chat/completions" {
		t.Errorf("endpoint = %q, want .../chat/completions", got)
	}
}

func TestEndpointPreservesFullPath(t *testing.T) {
	p := newTestOA(t, "m")
	p.baseURL = "http://example/v1/chat/completions"
	if got := p.endpoint(); got != "http://example/v1/chat/completions" {
		t.Errorf("endpoint should be unchanged when already complete: %q", got)
	}
}

func TestBuildPayloadFallsBackToProviderMaxTokens(t *testing.T) {
	p := newTestOA(t, "m")
	// No request max_tokens -> provider default (2048 when unset).
	pl, err := p.buildPayload(Request{Image: []byte("x"), Prompt: "q"})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if pl["max_tokens"] != 2048 {
		t.Errorf("max_tokens fallback = %v, want 2048", pl["max_tokens"])
	}
	if pl["model"] != "m" {
		t.Errorf("model = %v, want m", pl["model"])
	}
}

func TestBuildPayloadRequestMaxTokensWins(t *testing.T) {
	p := newTestOA(t, "m")
	pl, err := p.buildPayload(Request{Image: []byte("x"), Prompt: "q", MaxTokens: 512})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if pl["max_tokens"] != 512 {
		t.Errorf("request max_tokens should win: %v", pl["max_tokens"])
	}
}

func TestBuildPayloadJSONOnlySetsLowTemperature(t *testing.T) {
	p := newTestOA(t, "m")
	pl, err := p.buildPayload(Request{Image: []byte("x"), Prompt: "q", JSONOnly: true})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if pl["temperature"] != 0.1 {
		t.Errorf("JSONOnly must pin temperature to 0.1 for reproducibility: %v", pl["temperature"])
	}
}

func TestBuildPayloadRejectsEmptyImage(t *testing.T) {
	p := newTestOA(t, "m")
	if _, err := p.buildPayload(Request{Prompt: "q"}); err == nil {
		t.Errorf("buildPayload must reject an empty image")
	}
}

func TestSetAuthVariants(t *testing.T) {
	cases := []struct {
		method   string
		apiKey   string
		header   string
		wantAuth string
	}{
		{"", "k", "", "Bearer k"},
		{"bearer", "k", "", "Bearer k"},
		{"basic", "k", "", "Basic k"},
		{"custom_header", "k", "X-Api-Key", "k"}, // value placed under custom header
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
		o := &openAIVision{apiKey: c.apiKey, authMethod: c.method, customHeader: c.header}
		o.setAuth(req)
		switch {
		case c.method == "custom_header" && c.header != "":
			if got := req.Header.Get(c.header); got != c.apiKey {
				t.Errorf("custom_header: %s = %q, want %q", c.header, got, c.apiKey)
			}
			if req.Header.Get("Authorization") != "" {
				t.Errorf("custom_header must not also set Authorization")
			}
		default:
			if got := req.Header.Get("Authorization"); got != c.wantAuth {
				t.Errorf("auth %s: Authorization = %q, want %q", c.method, got, c.wantAuth)
			}
		}
	}
}

func TestSetAuthSkippedWhenNoKey(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	o := &openAIVision{apiKey: ""}
	o.setAuth(req)
	if req.Header.Get("Authorization") != "" {
		t.Errorf("local backend with no key must send no Authorization header")
	}
}

func TestParseChatCompletionStringContent(t *testing.T) {
	body := `{"model":"m","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	resp, err := parseChatCompletion([]byte(body), "prov", "fallback")
	if err != nil {
		t.Fatalf("parseChatCompletion: %v", err)
	}
	if resp.Text != "hello" {
		t.Errorf("text = %q, want hello", resp.Text)
	}
	if resp.Model != "m" {
		t.Errorf("model = %q, want m", resp.Model)
	}
	if resp.PromptTokens != 3 || resp.CompletionTokens != 2 {
		t.Errorf("usage not parsed: %+v", resp)
	}
}

func TestParseChatCompletionArrayContent(t *testing.T) {
	body := `{"model":"m","choices":[{"message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}]}`
	resp, err := parseChatCompletion([]byte(body), "prov", "fallback")
	if err != nil {
		t.Fatalf("parseChatCompletion: %v", err)
	}
	if resp.Text != "ab" {
		t.Errorf("array content should concatenate: %q", resp.Text)
	}
}

func TestParseChatCompletionModelFallback(t *testing.T) {
	body := `{"choices":[{"message":{"content":"x"}}]}`
	resp, err := parseChatCompletion([]byte(body), "prov", "fallback-model")
	if err != nil {
		t.Fatalf("parseChatCompletion: %v", err)
	}
	if resp.Model != "fallback-model" {
		t.Errorf("model should fall back when omitted: %q", resp.Model)
	}
}

func TestParseChatCompletionErrorObject(t *testing.T) {
	body := `{"error":{"message":"rate limited","type":"rate_limit"}}`
	if _, err := parseChatCompletion([]byte(body), "prov", "fb"); err == nil {
		t.Errorf("HTTP 200 with error object must be treated as failure")
	}
}

func TestParseChatCompletionNoChoices(t *testing.T) {
	body := `{"model":"m","choices":[]}`
	if _, err := parseChatCompletion([]byte(body), "prov", "fb"); err == nil {
		t.Errorf("empty choices must be a failure")
	}
}

func TestParseChatCompletionEmptyContent(t *testing.T) {
	body := `{"choices":[{"message":{"content":""}}]}`
	if _, err := parseChatCompletion([]byte(body), "prov", "fb"); err == nil {
		t.Errorf("empty content must be a failure")
	}
}

func TestIsRetryable(t *testing.T) {
	if !isRetryable(retryable{error: errors.New("transient")}) {
		t.Errorf("retryable wrapper must be recognised")
	}
	if isRetryable(errors.New("permanent")) {
		t.Errorf("plain error must not be retryable")
	}
}

func TestDoOnceRetriesThenSucceeds(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	p, err := newOpenAIVision("test", &config.ProviderConfig{
		Type: TypeOpenAIVision, BaseURL: srv.URL, Model: "m",
	})
	if err != nil {
		t.Fatalf("newOpenAIVision: %v", err)
	}
	o := p.(*openAIVision)
	o.client = srv.Client()
	resp, err := o.Complete(context.Background(), Request{Image: []byte("x"), ImageMIME: "image/png", Prompt: "q"})
	if err != nil {
		t.Fatalf("Complete should succeed after retries: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("body = %q, want ok", resp.Text)
	}
	if hits != 3 {
		t.Errorf("server should have been hit 3 times (2 failures + 1 success): %d", hits)
	}
}

func TestDoOnceNonRetryableStopsImmediately(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer srv.Close()

	p, err := newOpenAIVision("test", &config.ProviderConfig{
		Type: TypeOpenAIVision, BaseURL: srv.URL, Model: "m",
	})
	if err != nil {
		t.Fatalf("newOpenAIVision: %v", err)
	}
	o := p.(*openAIVision)
	o.client = srv.Client()
	if _, err := o.doOnce(context.Background(), []byte("{}")); err == nil {
		t.Errorf("400 must not be retried")
	}
	if hits != 1 {
		t.Errorf("non-retryable status must hit the server exactly once: %d", hits)
	}
}

func TestDoOnceTruncatesLargeErrorBody(t *testing.T) {
	big := strings.Repeat("X", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()
	p, _ := newOpenAIVision("test", &config.ProviderConfig{
		Type: TypeOpenAIVision, BaseURL: srv.URL, Model: "m",
	})
	o := p.(*openAIVision)
	o.client = srv.Client()
	_, err := o.doOnce(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(err.Error()) > maxErrorBody+60 {
		t.Errorf("error body not truncated: len=%d", len(err.Error()))
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		head string
		want time.Duration
	}{
		{"delta seconds", "7", 7 * time.Second},
		{"zero", "0", 0},
		{"negative", "-3", 0},
		{"past http date", time.Now().UTC().Add(-time.Hour).Format(http.TimeFormat), 0},
		{"garbage", "soon", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.head); got != c.want {
			t.Errorf("%s: parseRetryAfter(%q) = %v, want %v", c.name, c.head, got, c.want)
		}
	}
	future := time.Now().UTC().Add(3 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 6*time.Second {
		t.Errorf("future http date: got %v, want (0, 6s]", got)
	}
}

func TestRetryDelayPrefersRetryAfter(t *testing.T) {
	err := retryable{error: errors.New("429"), after: 4 * time.Second}
	if got := retryDelay(err, 1); got != 4*time.Second {
		t.Errorf("Retry-After must override backoff: %v", got)
	}
	capped := retryable{error: errors.New("429"), after: maxRetryWait + time.Minute}
	if got := retryDelay(capped, 1); got != maxRetryWait {
		t.Errorf("Retry-After must be capped at maxRetryWait: %v", got)
	}
}

func TestRetryDelayExponentialFallback(t *testing.T) {
	got := []time.Duration{
		retryDelay(retryable{error: errors.New("500")}, 1),
		retryDelay(errors.New("transport"), 2),
		retryDelay(retryable{error: errors.New("500"), after: 0}, 3),
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("attempt %d: retryDelay = %v, want %v", i+1, g, want[i])
		}
	}
	if d := retryDelay(retryable{error: errors.New("500")}, 40); d != maxRetryWait {
		t.Errorf("deep attempt must saturate at maxRetryWait, got %v", d)
	}
}

func TestCompleteHonoursRetryAfterOn429(t *testing.T) {
	const hint = 1 // seconds; must actually be waited out
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", strconv.Itoa(hint))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	p, err := newOpenAIVision("test", &config.ProviderConfig{
		Type: TypeOpenAIVision, BaseURL: srv.URL, Model: "m",
	})
	if err != nil {
		t.Fatalf("newOpenAIVision: %v", err)
	}
	o := p.(*openAIVision)
	o.client = srv.Client()

	start := time.Now()
	resp, err := o.Complete(context.Background(), Request{Image: []byte("x"), ImageMIME: "image/png", Prompt: "q"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("429 with Retry-After must be retried: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("body = %q, want ok", resp.Text)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	if elapsed < time.Duration(hint)*time.Second {
		t.Errorf("Retry-After of %ds not honoured, waited only %v", hint, elapsed)
	}
}

func TestCompleteGivesUpAfterMaxAttemptsOn429(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p, _ := newOpenAIVision("test", &config.ProviderConfig{
		Type: TypeOpenAIVision, BaseURL: srv.URL, Model: "m",
	})
	o := p.(*openAIVision)
	o.client = srv.Client()
	if _, err := o.Complete(context.Background(), Request{Image: []byte("x"), Prompt: "q"}); err == nil {
		t.Fatalf("persistent 429 must exhaust retries")
	}
	if hits != maxAttempts {
		t.Errorf("hits = %d, want %d", hits, maxAttempts)
	}
}
