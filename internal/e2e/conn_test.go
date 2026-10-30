package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeServer reads NDJSON requests from in and writes back whatever frames
// reply returns for each. Tests script it to play a server that answers,
// misbehaves, or stays silent.
func fakeServer(t *testing.T, in io.Reader, out io.Writer, reply func(id, method string) []string) {
	t.Helper()
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Errorf("fake server: bad request %q: %v", line, err)
			return
		}
		id := strings.TrimSpace(string(req.ID))
		for _, frame := range reply(id, req.Method) {
			if _, err := io.WriteString(out, frame+"\n"); err != nil {
				t.Errorf("fake server: write: %v", err)
				return
			}
		}
	}
}

// pipeConn wires a Conn to a scripted fake server through two in-memory pipes.
func pipeConn(t *testing.T, reply func(id, method string) []string) *Conn {
	t.Helper()
	reqR, reqW := io.Pipe()   // client -> server
	respR, respW := io.Pipe() // server -> client
	go fakeServer(t, reqR, respW, reply)
	t.Cleanup(func() {
		reqW.CloseWithError(io.EOF)
		respW.CloseWithError(io.EOF)
	})
	return NewConn(respR, reqW)
}

func TestCallRoundTrip(t *testing.T) {
	conn := pipeConn(t, func(id, method string) []string {
		return []string{`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2024-11-05"}}`}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := conn.Call(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := resp.ID.String(); got != "1" {
		t.Errorf("response id = %q, want 1", got)
	}
}

func TestCallSkipsForeignFrames(t *testing.T) {
	conn := pipeConn(t, func(id, method string) []string {
		return []string{
			`{"jsonrpc":"2.0","method":"notifications/log"}`,
			`{"jsonrpc":"2.0","id":999,"result":{"wrong":true}}`,
			`{"jsonrpc":"2.0","id":` + id + `,"result":{"ok":true}}`,
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := conn.Call(ctx, "tools/call", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Errorf("got the wrong response: %s", raw)
	}
}

func TestCallTimesOutWhenServerSilent(t *testing.T) {
	conn := pipeConn(t, func(id, method string) []string { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := conn.Call(ctx, "tools/call", nil); err == nil {
		t.Fatal("Call against a silent server: want timeout error, got nil")
	}
}

func TestConnFailsOnGarbageFrame(t *testing.T) {
	conn := pipeConn(t, func(id, method string) []string {
		return []string{"this is not json"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := conn.Call(ctx, "initialize", nil)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON-RPC") {
		t.Fatalf("want protocol-violation error, got %v", err)
	}
}

func TestConnFailsOnCleanEOF(t *testing.T) {
	conn := NewConn(strings.NewReader(""), &strings.Builder{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := conn.Call(ctx, "initialize", nil); err == nil {
		t.Fatal("Call against a closed server: want EOF error, got nil")
	}
}

func TestNotifyOmitsID(t *testing.T) {
	var buf strings.Builder
	conn := NewConn(strings.NewReader(""), &buf)

	if err := conn.Notify("notifications/initialized", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	frame := buf.String()
	if strings.Contains(frame, `"id"`) {
		t.Errorf("notification carries an id: %s", frame)
	}
	if !strings.Contains(frame, `"notifications/initialized"`) {
		t.Errorf("method missing from frame: %s", frame)
	}
}
