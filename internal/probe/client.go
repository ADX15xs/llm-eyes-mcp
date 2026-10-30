package probe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// systemPrompt is kept verbatim from the retired .mjs probe, which validated
// it against the gateway, so results stay comparable across the rewrite.
const systemPrompt = `You are a region locator for a design-revert verification instrument. Your ONLY job is to locate regions described in the input and report their bounding boxes plus any text you see inside them.

Rules:
1. Reply with RAW JSON only. No markdown fences, no prose, no explanation.
2. Schema: {"regions":[{"id":"<region_id_from_spec>","bbox":[x0,y0,x1,y1],"text":"<text inside region>"}]}
3. Coordinates are NORMALISED floats in [0,1]: x0,y0 = top-left corner, x1,y1 = bottom-right corner. Origin is the top-left of the image.
4. Use EXACTLY the id strings the spec gives you, one entry per region. Never invent extra regions, never merge two.
5. The downstream system will compare your boxes against the spec and compute colour/text deltas in code. Accurate boxes matter more than anything else.
6. If a region is genuinely absent from this screenshot, still emit its entry with bbox [0,0,0,0] and text "".
7. Do not estimate distances, sizes or colours. Boxes and text only.`

// LocatePrompt builds the per-case user prompt from the probe targets.
func LocatePrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Locate the following %d region(s) in this screenshot and return their bounding boxes plus any text inside. Return raw JSON only:\n", len(Targets))
	for _, t := range Targets {
		fmt.Fprintf(&b, "  - id=%s desc=TEXT %s text=%s\n", t.ID, t.Label, t.Text)
	}
	b.WriteString(`Return only: {"regions":[{"id":"...","bbox":[x0,y0,x1,y1],"text":"..."}]}`)
	return b.String()
}

// CallOptions describes one probe call.
type CallOptions struct {
	BaseURL  string
	APIKey   string
	Model    string
	PNG      []byte
	Thinking string // "" = no nvext (full thinking); "0"/"512" cap the budget
	Timeout  time.Duration
}

// CallResult is the outcome of one call; Error is set whenever the upstream
// misbehaved and no scoring is possible.
type CallResult struct {
	Milliseconds     int64
	Status           int
	Finish           string
	Content          string
	Parsed           *Reply
	ReasoningTokens  *int
	CompletionTokens *int
	Error            string
}

var httpClient = &http.Client{} // deadline comes from the per-call context

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string for system; []contentPart for user
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type nvExt struct {
	MaxThinkingTokens int `json:"max_thinking_tokens"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	NVExt       *nvExt        `json:"nvext,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// Call performs one OpenAI-compatible chat completion with the rendered case.
// It never returns a Go error: upstream failures travel in CallResult.Error so
// the probe loop can log them and move to the next sample.
func Call(ctx context.Context, opts CallOptions) *CallResult {
	body := chatRequest{
		Model: opts.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: []contentPart{
				{Type: "text", Text: LocatePrompt()},
				{Type: "image_url", ImageURL: &imageURL{
					URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(opts.PNG),
				}},
			}},
		},
		MaxTokens:   2048,
		Temperature: 0.1,
	}
	if opts.Thinking != "" {
		n, err := strconv.Atoi(opts.Thinking)
		if err != nil {
			return &CallResult{Error: "bad thinking budget: " + opts.Thinking}
		}
		body.NVExt = &nvExt{MaxThinkingTokens: n}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return &CallResult{Error: "marshal request: " + err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return &CallResult{Error: "build request: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	req.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	res, err := httpClient.Do(req)
	ms := time.Since(t0).Milliseconds()
	if err != nil {
		return &CallResult{Milliseconds: ms, Error: err.Error()}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return &CallResult{Milliseconds: ms, Status: res.StatusCode, Error: err.Error()}
	}

	out := &CallResult{Milliseconds: ms, Status: res.StatusCode}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		out.Error = "non-JSON: " + truncate(string(raw), 160)
		return out
	}
	if len(parsed.Choices) == 0 {
		out.Error = truncate(string(raw), 200)
		return out
	}
	choice := parsed.Choices[0]
	out.Finish = choice.FinishReason
	out.Content = choice.Message.Content
	out.CompletionTokens = &parsed.Usage.CompletionTokens
	if d := parsed.Usage.CompletionTokensDetails; d != nil {
		out.ReasoningTokens = &d.ReasoningTokens
	}
	var reply Reply
	if json.Unmarshal([]byte(choice.Message.Content), &reply) == nil {
		out.Parsed = &reply
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
