package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// logPrefix tags every stderr line so clients can separate our logs from noise.
const logPrefix = "[llm-eyes-mcp]"

// ServerInfo is reported back during initialize.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server speaks JSON-RPC 2.0 over stdio using NDJSON framing.
//
// Requests are dispatched concurrently (one goroutine each) so a slow VLM call
// cannot block ping or other tools. Because of that, every stdout write goes
// through writeMu - interleaved writes would corrupt the stream.
type Server struct {
	info      ServerInfo
	tools     *Registry
	instructs string

	in  io.Reader
	out io.Writer
	log io.Writer

	writeMu sync.Mutex
	wg      sync.WaitGroup
}

// Option customises a Server.
type Option func(*Server)

// WithIO overrides the input/output streams (used by tests).
func WithIO(in io.Reader, out io.Writer) Option {
	return func(s *Server) { s.in, s.out = in, out }
}

// WithLogWriter overrides the stderr log sink.
func WithLogWriter(w io.Writer) Option {
	return func(s *Server) { s.log = w }
}

// WithInstructions sets the server-level guidance returned by initialize. This
// is where the capability-boundary statement belongs so every client sees it.
func WithInstructions(text string) Option {
	return func(s *Server) { s.instructs = text }
}

// NewServer builds a server around a tool registry.
func NewServer(info ServerInfo, tools *Registry, opts ...Option) *Server {
	s := &Server{
		info:  info,
		tools: tools,
		in:    os.Stdin,
		out:   os.Stdout,
		log:   os.Stderr,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Logf writes a diagnostic line to stderr. Never to stdout: stdout is the
// protocol channel and a single stray print corrupts the whole session.
func (s *Server) Logf(format string, a ...any) {
	fmt.Fprintf(s.log, logPrefix+" "+format+"\n", a...)
}

// Start runs the read/dispatch loop until stdin reaches EOF.
func (s *Server) Start() error {
	reader := bufio.NewReader(s.in)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// A final line without a trailing newline is still a valid message.
			if trimmed := trimFrame(line); trimmed != "" {
				s.dispatchLine(trimmed)
			}
			if err == io.EOF {
				break
			}
			s.wg.Wait()
			return fmt.Errorf("read stdin: %w", err)
		}
		line = trimFrame(line)
		if line == "" {
			continue // blank keep-alive line, not an error
		}
		s.dispatchLine(line)
	}
	s.wg.Wait()
	return nil
}

// trimFrame strips trailing CR/LF so \r\n clients work unchanged.
func trimFrame(s string) string {
	return strings.TrimRight(s, "\r\n")
}

// dispatchLine parses one NDJSON line and routes it. A malformed message must
// only produce an error response - it must never terminate the loop.
func (s *Server) dispatchLine(line string) {
	var msg Message
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		s.sendError(NewNullID(), CodeParseError, "Parse error: "+err.Error())
		return
	}

	switch msg.Method {
	case "initialized", "notifications/initialized", "notifications/cancelled":
		// Notifications carry no id: answering them desynchronises the client.
		return
	}
	if msg.IsNotification() {
		// Any other notification is likewise acknowledged with silence.
		return
	}

	// Long-running tool calls run concurrently so ping stays responsive.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.Logf("panic handling %s: %v", msg.Method, r)
				s.sendError(msg.ID, CodeInternalError,
					fmt.Sprintf("Internal error handling %s", msg.Method))
			}
		}()
		s.handle(&msg)
	}()
}

func (s *Server) handle(msg *Message) {
	switch msg.Method {
	case "initialize":
		s.handleInitialize(msg)
	case "tools/list":
		s.handleToolsList(msg)
	case "tools/call":
		s.handleToolCall(msg)
	case "ping":
		s.sendResult(msg.ID, map[string]any{})
	default:
		s.sendError(msg.ID, CodeMethodNotFound, "Method not found: "+msg.Method)
	}
}

func (s *Server) handleInitialize(msg *Message) {
	result := map[string]any{
		"protocolVersion": ProtocolVersion,
		// Declare only what is actually implemented.
		"capabilities": map[string]any{"tools": map[string]any{}},
		"serverInfo":   s.info,
	}
	if s.instructs != "" {
		result["instructions"] = s.instructs
	}
	s.sendResult(msg.ID, result)
}

func (s *Server) handleToolsList(msg *Message) {
	tools := s.tools.List()
	// Must be a non-nil slice so an empty registry serialises as [] not null.
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"inputSchema": t.InputSchema(),
		})
	}
	s.sendResult(msg.ID, map[string]any{"tools": out})
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleToolCall(msg *Message) {
	if len(msg.Params) == 0 {
		s.sendError(msg.ID, CodeInvalidParams, "Invalid params: params is required")
		return
	}
	var p toolCallParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		s.sendError(msg.ID, CodeInvalidParams, "Invalid params: "+err.Error())
		return
	}
	if p.Name == "" {
		s.sendError(msg.ID, CodeInvalidParams, "Invalid params: tool name is required")
		return
	}
	tool, ok := s.tools.Get(p.Name)
	if !ok {
		s.sendError(msg.ID, CodeInternalError, "Unknown tool: "+p.Name)
		return
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}

	res := tool.Call(context.Background(), p.Arguments)
	if res == nil {
		res = ErrorResult("Tool " + p.Name + " returned no result")
	}
	if len(res.Content) == 0 {
		// Never return an empty content array - some clients choke on it.
		res.Content = []map[string]any{TextContent("No results.")}
	}
	s.sendResult(msg.ID, res)
}

func (s *Server) sendResult(id *ID, result any) {
	s.write(&Response{JSONRPC: "2.0", ID: normalizeID(id), Result: result})
}

func (s *Server) sendError(id *ID, code int, message string) {
	s.write(&Response{
		JSONRPC: "2.0",
		ID:      normalizeID(id),
		Error:   &RPCError{Code: code, Message: message},
	})
}

func normalizeID(id *ID) *ID {
	if id == nil {
		return NewNullID()
	}
	return id
}

// write emits one NDJSON frame. Serialised by writeMu because handlers run
// concurrently.
func (s *Server) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.Logf("marshal response: %v", err)
		return
	}
	data = append(data, '\n')

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(data); err != nil {
		s.Logf("write response: %v", err)
		return
	}
	if f, ok := s.out.(interface{ Sync() error }); ok {
		_ = f.Sync() // *os.File: flush so the client sees the frame immediately
	}
}
