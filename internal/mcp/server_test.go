package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const readTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// mock tool
// ---------------------------------------------------------------------------

type mockTool struct {
	name   string
	desc   string
	schema map[string]any
	fn     func(ctx context.Context, args map[string]any) *ToolResult

	mu    sync.Mutex
	calls []map[string]any
}

func newMockTool(name string) *mockTool {
	return &mockTool{
		name: name,
		desc: name + " description",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"image_source": map[string]any{"type": "string", "description": "image"},
				"labels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"image_source"},
		},
	}
}

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return m.desc }
func (m *mockTool) InputSchema() map[string]any { return m.schema }
func (m *mockTool) Call(ctx context.Context, args map[string]any) *ToolResult {
	m.mu.Lock()
	m.calls = append(m.calls, args)
	m.mu.Unlock()
	if m.fn != nil {
		return m.fn(ctx, args)
	}
	return TextResult(m.name + " ok")
}

func (m *mockTool) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockTool) lastArgs() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	return m.calls[len(m.calls)-1]
}

// ---------------------------------------------------------------------------
// stdio harness: real os.Pipe, real Start() loop, timeout-guarded reads
// ---------------------------------------------------------------------------

type rawResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

type session struct {
	t       *testing.T
	stdin   *io.PipeWriter
	lines   chan string
	srvDone chan error
	logs    *strings.Builder
	logMu   sync.Mutex
}

func newSession(t *testing.T, reg *Registry, opts ...Option) *session {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := &session{
		t:       t,
		stdin:   inW,
		lines:   make(chan string, 32),
		srvDone: make(chan error, 1),
		logs:    &strings.Builder{},
	}

	all := append([]Option{WithIO(inR, outW), WithLogWriter(&lockedWriter{b: s.logs, mu: &s.logMu})}, opts...)
	srv := NewServer(ServerInfo{Name: "test-server", Version: "9.9.9"}, reg, all...)

	go func() {
		err := srv.Start()
		_ = outW.Close()
		s.srvDone <- err
	}()

	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()

	t.Cleanup(func() { s.close() })
	return s
}

type lockedWriter struct {
	b  *strings.Builder
	mu *sync.Mutex
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (s *session) close() {
	_ = s.stdin.Close()
	select {
	case <-s.srvDone:
	case <-time.After(readTimeout):
		s.t.Error("server did not exit after stdin close")
	}
}

// sendRaw writes an exact line to the server, bypassing JSON encoding so
// malformed input can be tested.
func (s *session) sendRaw(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		s.t.Fatalf("write stdin: %v", err)
	}
}

func (s *session) send(id any, method string, params any) {
	s.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		s.t.Fatalf("marshal request: %v", err)
	}
	s.sendRaw(string(data))
}

// read returns the next frame, failing the test on timeout instead of hanging.
func (s *session) read() *rawResponse {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatal("stdout closed before a response arrived")
		}
		var r rawResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			s.t.Fatalf("response is not valid JSON: %v\nline: %s", err, line)
		}
		if r.JSONRPC != "2.0" {
			s.t.Errorf("jsonrpc = %q, want \"2.0\"", r.JSONRPC)
		}
		return &r
	case <-time.After(readTimeout):
		s.t.Fatal("timeout reading response")
		return nil
	}
}

func (s *session) call(id any, method string, params any) *rawResponse {
	s.t.Helper()
	s.send(id, method, params)
	return s.read()
}

// expectSilence proves a notification produced no frame at all.
func (s *session) expectSilence(d time.Duration) {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if ok {
			s.t.Fatalf("expected silence, got frame: %s", line)
		}
	case <-time.After(d):
	}
}

func (s *session) stderr() string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.logs.String()
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode result: %v\nraw: %s", err, raw)
	}
	return v
}

func registryWith(tools ...Tool) *Registry {
	r := NewRegistry()
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// ---------------------------------------------------------------------------
// initialize
// ---------------------------------------------------------------------------

func TestInitializeReturnsAllFields(t *testing.T) {
	s := newSession(t, registryWith(newMockTool("measure_image")))
	resp := s.call(1, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.0"},
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", resp.ID)
	}

	result := decode[map[string]any](t, resp.Result)
	if got := result["protocolVersion"]; got != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", got, ProtocolVersion)
	}

	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo missing or wrong type: %#v", result["serverInfo"])
	}
	if info["name"] != "test-server" || info["version"] != "9.9.9" {
		t.Errorf("serverInfo = %#v", info)
	}

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing: %#v", result["capabilities"])
	}
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities.tools missing: %#v", caps)
	}
	// Only implemented capabilities may be declared.
	for _, unimplemented := range []string{"resources", "prompts", "sampling"} {
		if _, ok := caps[unimplemented]; ok {
			t.Errorf("declared unimplemented capability %q", unimplemented)
		}
	}
}

func TestInitializeCarriesInstructions(t *testing.T) {
	const boundary = "this server measures geometry, it does not judge aesthetics"
	s := newSession(t, registryWith(), WithInstructions(boundary))
	resp := s.call(1, "initialize", nil)

	result := decode[map[string]any](t, resp.Result)
	if got, _ := result["instructions"].(string); got != boundary {
		t.Errorf("instructions = %q, want %q", got, boundary)
	}
}

func TestInitializePreservesStringID(t *testing.T) {
	s := newSession(t, registryWith())
	resp := s.call("req-abc", "initialize", nil)
	if string(resp.ID) != `"req-abc"` {
		t.Errorf("id = %s, want \"req-abc\" (string ids must round-trip unchanged)", resp.ID)
	}
}

// ---------------------------------------------------------------------------
// notifications - must be answered with total silence
// ---------------------------------------------------------------------------

func TestNotificationsProduceNoResponse(t *testing.T) {
	notifications := []string{
		"initialized",
		"notifications/initialized",
		"notifications/cancelled",
		"notifications/progress", // any other notification too
	}
	for _, method := range notifications {
		t.Run(method, func(t *testing.T) {
			s := newSession(t, registryWith(newMockTool("measure_image")))
			s.send(nil, method, map[string]any{"anything": true}) // no id => notification
			s.expectSilence(400 * time.Millisecond)

			// The loop must still be alive afterwards.
			resp := s.call(7, "ping", nil)
			if resp.Error != nil {
				t.Fatalf("server broke after notification: %+v", resp.Error)
			}
		})
	}
}

func TestInitializeThenNotificationHandshake(t *testing.T) {
	s := newSession(t, registryWith())
	if resp := s.call(1, "initialize", nil); resp.Error != nil {
		t.Fatalf("initialize failed: %+v", resp.Error)
	}
	// The real client sends this immediately after initialize, with no id.
	s.sendRaw(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.expectSilence(400 * time.Millisecond)

	if resp := s.call(2, "tools/list", nil); resp.Error != nil {
		t.Fatalf("tools/list after handshake failed: %+v", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// tools/list
// ---------------------------------------------------------------------------

func TestToolsListShape(t *testing.T) {
	s := newSession(t, registryWith(newMockTool("measure_image")))
	resp := s.call(2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result := decode[struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}](t, resp.Result)

	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}
	tool := result.Tools[0]
	if tool.Name != "measure_image" {
		t.Errorf("name = %q", tool.Name)
	}
	if tool.Description == "" {
		t.Error("description must not be empty: the agent routes on it")
	}
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema.properties missing: %#v", tool.InputSchema)
	}
	if _, ok := props["image_source"]; !ok {
		t.Errorf("properties.image_source missing: %#v", props)
	}
	req, ok := tool.InputSchema["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "image_source" {
		t.Errorf("required = %#v, want [image_source]", tool.InputSchema["required"])
	}
}

func TestToolsListEmptyRegistryReturnsEmptyArray(t *testing.T) {
	s := newSession(t, registryWith())
	resp := s.call(2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("empty registry must not error: %+v", resp.Error)
	}
	// Must serialise as [] and never null.
	if !strings.Contains(string(resp.Result), `"tools":[]`) {
		t.Errorf("result = %s, want tools:[]", resp.Result)
	}
}

func TestToolsListOrderIsDeterministic(t *testing.T) {
	// Registered out of order on purpose: Go map iteration is random.
	reg := registryWith(
		newMockTool("measure_image"),
		newMockTool("describe_image"),
		newMockTool("extract_text"),
	)
	var first []string
	for i := 0; i < 5; i++ {
		s := newSession(t, reg)
		resp := s.call(1, "tools/list", nil)
		result := decode[struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}](t, resp.Result)

		names := make([]string, 0, len(result.Tools))
		for _, tl := range result.Tools {
			names = append(names, tl.Name)
		}
		if i == 0 {
			first = names
			continue
		}
		if !reflect.DeepEqual(names, first) {
			t.Fatalf("tools/list order changed between calls: %v vs %v", names, first)
		}
	}
	want := []string{"describe_image", "extract_text", "measure_image"} // sorted
	if !reflect.DeepEqual(first, want) {
		t.Errorf("order = %v, want %v", first, want)
	}
}

// ---------------------------------------------------------------------------
// tools/call
// ---------------------------------------------------------------------------

func TestToolCallSuccess(t *testing.T) {
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		return TextResult("Distance: 42.50 px")
	}
	s := newSession(t, registryWith(tool))

	resp := s.call(3, "tools/call", map[string]any{
		"name":      "measure_image",
		"arguments": map[string]any{"image_source": "/tmp/a.png"},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}

	result := decode[struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
	}](t, resp.Result)

	if result.IsError {
		t.Error("isError = true, want false")
	}
	if len(result.Content) == 0 {
		t.Fatal("content is empty")
	}
	if result.Content[0]["type"] != "text" {
		t.Errorf("content[0].type = %v, want text", result.Content[0]["type"])
	}
	if text, _ := result.Content[0]["text"].(string); !strings.Contains(text, "42.50") {
		t.Errorf("content[0].text = %q, want it to contain the result", text)
	}
}

// A business failure must be readable by the agent, so it travels in the
// result as isError - never as a JSON-RPC error, which agents cannot retry on.
func TestToolCallBusinessFailureUsesIsError(t *testing.T) {
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		return ErrorResult("VLM could not locate label \"logo\"")
	}
	s := newSession(t, registryWith(tool))

	resp := s.call(4, "tools/call", map[string]any{
		"name":      "measure_image",
		"arguments": map[string]any{"image_source": "x"},
	})
	if resp.Error != nil {
		t.Fatalf("business failure must NOT produce a JSON-RPC error, got %+v", resp.Error)
	}

	result := decode[struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
	}](t, resp.Result)

	if !result.IsError {
		t.Error("isError = false, want true")
	}
	text, _ := result.Content[0]["text"].(string)
	if !strings.Contains(text, "logo") {
		t.Errorf("error text = %q, want the reason to be visible to the agent", text)
	}
}

func TestToolCallUnknownToolReturns32603(t *testing.T) {
	s := newSession(t, registryWith(newMockTool("measure_image")))
	resp := s.call(5, "tools/call", map[string]any{"name": "no_such_tool"})
	if resp.Error == nil {
		t.Fatal("want an error")
	}
	if resp.Error.Code != CodeInternalError {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeInternalError)
	}
	if !strings.Contains(resp.Error.Message, "no_such_tool") {
		t.Errorf("message = %q, want the tool name", resp.Error.Message)
	}
}

func TestToolCallInvalidParamsReturns32602(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"no params at all", `{"jsonrpc":"2.0","id":6,"method":"tools/call"}`},
		{"params is a string", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":"oops"}`},
		{"params is an array", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":[1,2]}`},
		{"empty tool name", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":""}}`},
		{"name wrong type", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":123}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t, registryWith(newMockTool("measure_image")))
			s.sendRaw(tc.line)
			resp := s.read()
			if resp.Error == nil {
				t.Fatalf("want an error, got result %s", resp.Result)
			}
			if resp.Error.Code != CodeInvalidParams {
				t.Errorf("code = %d, want %d (%s)", resp.Error.Code, CodeInvalidParams, resp.Error.Message)
			}
		})
	}
}

func TestToolCallMissingArgumentsDefaultsToEmptyMap(t *testing.T) {
	tool := newMockTool("describe_image")
	tool.fn = func(_ context.Context, args map[string]any) *ToolResult {
		if args == nil {
			return ErrorResult("arguments was nil - tools must never receive a nil map")
		}
		return TextResult(fmt.Sprintf("got %d args", len(args)))
	}
	s := newSession(t, registryWith(tool))
	resp := s.call(1, "tools/call", map[string]any{"name": "describe_image"})

	result := decode[struct {
		Content []map[string]any `json:"content"`
		IsError bool             `json:"isError"`
	}](t, resp.Result)
	if result.IsError {
		t.Fatalf("nil arguments not normalised: %v", result.Content[0]["text"])
	}
}

func TestToolCallEmptyContentIsBackfilled(t *testing.T) {
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		return &ToolResult{} // no content at all
	}
	s := newSession(t, registryWith(tool))
	resp := s.call(1, "tools/call", map[string]any{"name": "measure_image"})

	result := decode[struct {
		Content []map[string]any `json:"content"`
	}](t, resp.Result)
	if len(result.Content) == 0 {
		t.Fatal("empty content array must be backfilled - some clients choke on it")
	}
}

func TestToolCallNilResultIsHandled(t *testing.T) {
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult { return nil }
	s := newSession(t, registryWith(tool))
	resp := s.call(1, "tools/call", map[string]any{"name": "measure_image"})
	if resp.Error != nil {
		t.Fatalf("nil ToolResult must not crash the protocol: %+v", resp.Error)
	}
	result := decode[struct {
		IsError bool `json:"isError"`
	}](t, resp.Result)
	if !result.IsError {
		t.Error("a nil result should surface as isError")
	}
}

// Arguments must reach the tool byte-for-byte, including the float64 typing
// that encoding/json gives every JSON number.
func TestArgumentPassthrough(t *testing.T) {
	tool := newMockTool("measure_image")
	s := newSession(t, registryWith(tool))

	s.call(1, "tools/call", map[string]any{
		"name": "measure_image",
		"arguments": map[string]any{
			"image_source": "https://example.com/a.png",
			"labels":       []any{"logo", "title"},
			"measure_type": "distance",
			"threshold":    12,   // JSON number -> float64
			"strict":       true, // bool
			"note":         nil,  // explicit null
		},
	})

	got := tool.lastArgs()
	if got == nil {
		t.Fatal("tool was never called")
	}
	if got["image_source"] != "https://example.com/a.png" {
		t.Errorf("image_source = %#v", got["image_source"])
	}
	if v, ok := got["threshold"].(float64); !ok || v != 12 {
		t.Errorf("threshold = %#v (%T), want float64(12)", got["threshold"], got["threshold"])
	}
	if v, ok := got["strict"].(bool); !ok || !v {
		t.Errorf("strict = %#v", got["strict"])
	}
	labels, ok := got["labels"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "logo" || labels[1] != "title" {
		t.Errorf("labels = %#v", got["labels"])
	}
	if v, present := got["note"]; !present || v != nil {
		t.Errorf("explicit null should arrive as a present nil, got %#v (present=%v)", v, present)
	}
}

// Calling A must not touch B.
func TestMultipleToolsAreIsolated(t *testing.T) {
	a := newMockTool("measure_image")
	b := newMockTool("extract_text")
	s := newSession(t, registryWith(a, b))

	s.call(1, "tools/call", map[string]any{"name": "measure_image", "arguments": map[string]any{"image_source": "x"}})
	if a.callCount() != 1 || b.callCount() != 0 {
		t.Fatalf("after calling A: a=%d b=%d, want 1/0", a.callCount(), b.callCount())
	}

	s.call(2, "tools/call", map[string]any{"name": "extract_text", "arguments": map[string]any{"image_source": "x"}})
	if a.callCount() != 1 || b.callCount() != 1 {
		t.Fatalf("after calling B: a=%d b=%d, want 1/1", a.callCount(), b.callCount())
	}
}

// ---------------------------------------------------------------------------
// error codes and loop robustness
// ---------------------------------------------------------------------------

func TestUnknownMethodReturns32601(t *testing.T) {
	s := newSession(t, registryWith())
	resp := s.call(8, "resources/list", nil)
	if resp.Error == nil {
		t.Fatal("want an error")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
}

func TestMalformedJSONReturns32700AndKeepsLoopAlive(t *testing.T) {
	s := newSession(t, registryWith(newMockTool("measure_image")))

	s.sendRaw(`{"jsonrpc":"2.0","id":9,"method":`) // truncated
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Fatalf("want -32700, got %+v", resp.Error)
	}
	// id is unrecoverable, so per spec it must be null - not omitted.
	if string(resp.ID) != "null" {
		t.Errorf("id = %s, want null", resp.ID)
	}

	// The main loop must survive: this is the whole point.
	if r := s.call(10, "ping", nil); r.Error != nil {
		t.Fatalf("loop died after malformed input: %+v", r.Error)
	}
}

func TestBlankLinesAreIgnored(t *testing.T) {
	s := newSession(t, registryWith())
	s.sendRaw("")
	s.sendRaw("   ")
	// "   " is not blank after trimming CR/LF only, so it parses as garbage.
	if resp := s.read(); resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Fatalf("whitespace-only line: want -32700, got %+v", resp.Error)
	}
	if r := s.call(1, "ping", nil); r.Error != nil {
		t.Fatalf("loop died: %+v", r.Error)
	}
}

func TestCRLFFramingIsAccepted(t *testing.T) {
	s := newSession(t, registryWith())
	if _, err := io.WriteString(s.stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if resp := s.read(); resp.Error != nil {
		t.Fatalf("\\r\\n framing rejected: %+v", resp.Error)
	}
}

func TestPanicInToolBecomes32603(t *testing.T) {
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		panic("boom")
	}
	s := newSession(t, registryWith(tool))
	resp := s.call(11, "tools/call", map[string]any{"name": "measure_image"})
	if resp.Error == nil || resp.Error.Code != CodeInternalError {
		t.Fatalf("want -32603, got %+v", resp.Error)
	}
	if !strings.Contains(s.stderr(), "panic") {
		t.Errorf("panic should be logged to stderr, got: %s", s.stderr())
	}
}

func TestPing(t *testing.T) {
	s := newSession(t, registryWith())
	resp := s.call(12, "ping", nil)
	if resp.Error != nil {
		t.Fatalf("ping failed: %+v", resp.Error)
	}
	if string(resp.Result) != "{}" {
		t.Errorf("ping result = %s, want {}", resp.Result)
	}
}

// A slow tool must not block ping: handlers run concurrently.
func TestSlowToolDoesNotBlockPing(t *testing.T) {
	release := make(chan struct{})
	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		<-release
		return TextResult("done")
	}
	s := newSession(t, registryWith(tool))

	s.send(1, "tools/call", map[string]any{"name": "measure_image"})
	s.send(2, "ping", nil)

	// ping must come back first, while the tool is still parked.
	resp := s.read()
	if string(resp.ID) != "2" {
		t.Fatalf("first frame id = %s, want the ping (2) to overtake the slow tool", resp.ID)
	}
	close(release)
	if resp := s.read(); string(resp.ID) != "1" {
		t.Fatalf("second frame id = %s, want 1", resp.ID)
	}
}

func TestEOFExitsCleanly(t *testing.T) {
	inR, inW := io.Pipe()
	var out strings.Builder
	srv := NewServer(ServerInfo{Name: "t", Version: "1"}, registryWith(),
		WithIO(inR, &out), WithLogWriter(io.Discard))

	done := make(chan error, 1)
	go func() { done <- srv.Start() }()

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"ping"}`) // no trailing \n
	_ = inW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v, want nil on EOF", err)
		}
	case <-time.After(readTimeout):
		t.Fatal("Start did not return on EOF")
	}
	// The final line without a newline must still have been processed.
	if !strings.Contains(out.String(), `"id":1`) {
		t.Errorf("last line without \\n was dropped: %q", out.String())
	}
}

// ---------------------------------------------------------------------------
// NDJSON framing invariants
// ---------------------------------------------------------------------------

func TestEveryFrameIsOneLineTerminatedByNewline(t *testing.T) {
	inR, inW := io.Pipe()
	var out strings.Builder
	srv := NewServer(ServerInfo{Name: "t", Version: "1"},
		registryWith(newMockTool("measure_image")),
		WithIO(inR, &syncWriter{b: &out}), WithLogWriter(io.Discard))

	done := make(chan error, 1)
	go func() { done <- srv.Start() }()

	for i := 1; i <= 3; i++ {
		_, _ = io.WriteString(inW, fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"ping\"}\n", i))
	}
	_ = inW.Close()
	<-done

	raw := out.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Error("stream does not end with a newline")
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d frames, want 3:\n%s", len(lines), raw)
	}
	for i, l := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Errorf("frame %d is not standalone JSON: %v", i, err)
		}
	}
}

type syncWriter struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// ---------------------------------------------------------------------------
// the dev-guide scaffold: swap the real os.Stdin / os.Stdout
// ---------------------------------------------------------------------------

// TestRealStdioRoundTrip exercises the default NewServer wiring (os.Stdin /
// os.Stdout) rather than the injected pipes every other test uses, so a
// regression in the production defaults cannot hide behind WithIO.
func TestRealStdioRoundTrip(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origStdin, origStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinReader, stdoutWriter
	t.Cleanup(func() { os.Stdin, os.Stdout = origStdin, origStdout })

	tool := newMockTool("measure_image")
	tool.fn = func(context.Context, map[string]any) *ToolResult {
		return TextResult("Distance: 7.00 px")
	}
	// No WithIO: the server must pick up os.Stdin / os.Stdout itself.
	srv := NewServer(ServerInfo{Name: "llm-eyes-mcp", Version: "test"},
		registryWith(tool), WithLogWriter(io.Discard))

	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.Start() }()

	frames := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdoutReader)
		for sc.Scan() {
			frames <- sc.Text()
		}
		close(frames)
	}()

	nextFrame := func() string {
		t.Helper()
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stdout closed early")
			}
			return f
		case <-time.After(readTimeout):
			t.Fatal("timeout reading response")
			return ""
		}
	}

	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(stdinWriter, s+"\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if got := nextFrame(); !strings.Contains(got, ProtocolVersion) {
		t.Errorf("initialize frame = %s", got)
	}

	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"measure_image","arguments":{"image_source":"x"}}}`)
	got := nextFrame()
	if !strings.Contains(got, "Distance: 7.00 px") {
		t.Errorf("tool frame = %s", got)
	}
	// The notification must not have produced a frame in between.
	if !strings.Contains(got, `"id":2`) {
		t.Errorf("unexpected extra frame before the tool result: %s", got)
	}

	_ = stdinWriter.Close()
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(readTimeout):
		t.Fatal("server did not exit")
	}
	_ = stdoutWriter.Close()
}
