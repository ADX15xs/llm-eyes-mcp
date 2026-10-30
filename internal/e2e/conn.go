// Package e2e is the Go replacement for the retired scripts/compare_e2e.mjs:
// it drives the compiled server binary over stdio exactly like a real MCP
// client, so `make e2e` can re-run the matched-pair acceptance (docs/TODO.md
// D-2) against the real gateway. Frame types come from internal/mcp so the
// client and the server cannot drift apart.
//
// This is a dev harness, not server code: it is never imported by the server
// and its tests use in-memory pipes only.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/xiaos/llm-eyes-mcp/internal/mcp"
)

// Conn is a JSON-RPC 2.0 client over NDJSON streams. Calls are strictly
// sequential — the harness never has more than one request in flight.
type Conn struct {
	w   io.Writer
	wmu sync.Mutex

	nextID int
	resps  chan mcp.Response
	errs   chan error
}

// respBuffer bounds frames read while nobody is calling; overflow means the
// peer is misbehaving, so the conn fails loudly instead of dropping silently.
const respBuffer = 8

// NewConn wraps a reader/writer pair (e.g. a spawned process's stdio) and
// starts reading frames in the background.
func NewConn(r io.Reader, w io.Writer) *Conn {
	c := &Conn{
		w:     w,
		resps: make(chan mcp.Response, respBuffer),
		errs:  make(chan error, 1),
	}
	go c.read(r)
	return c
}

// read forwards every NDJSON frame to resps. stdout is protocol-only by
// invariant, so an unparseable non-empty line is a protocol violation and
// fails the conn rather than being dropped — silent data loss is the enemy of
// an acceptance harness. The scanner buffer is raised because compare_design
// reports for large specs exceed bufio's 64KB default line cap.
func (c *Conn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp mcp.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			c.fail(fmt.Errorf("stdout frame is not valid JSON-RPC: %w", err))
			return
		}
		select {
		case c.resps <- resp:
		default:
			c.fail(fmt.Errorf("response buffer overflow (%d frames unconsumed)", respBuffer))
			return
		}
	}
	if err := sc.Err(); err != nil {
		c.fail(fmt.Errorf("read stdout: %w", err))
		return
	}
	c.fail(io.EOF) // clean EOF: the server closed stdout
}

// fail records a terminal error; the first one wins.
func (c *Conn) fail(err error) {
	select {
	case c.errs <- err:
	default:
	}
}

// Call sends one request and returns the response carrying the same id.
// Frames with other ids (the server sends none today) are skipped.
func (c *Conn) Call(ctx context.Context, method string, params any) (*mcp.Response, error) {
	c.nextID++
	id := numID(c.nextID)
	if err := c.send(id, method, params); err != nil {
		return nil, err
	}
	want := id.String()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for %s response: %w", method, ctx.Err())
		case err := <-c.errs:
			return nil, fmt.Errorf("%s: %w", method, err)
		case resp := <-c.resps:
			if resp.ID == nil || resp.ID.String() != want {
				continue
			}
			return &resp, nil
		}
	}
}

// Notify sends a notification: no id, therefore no response expected.
func (c *Conn) Notify(method string, params any) error {
	return c.send(nil, method, params)
}

func (c *Conn) send(id *mcp.ID, method string, params any) error {
	msg := mcp.Message{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		msg.Params = raw
	}
	frame, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", method, err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(append(frame, '\n')); err != nil {
		return fmt.Errorf("write %s request: %w", method, err)
	}
	return nil
}

// numID builds an id that marshals as a plain JSON number.
func numID(n int) *mcp.ID {
	var id mcp.ID
	_ = id.UnmarshalJSON([]byte(strconv.Itoa(n))) // cannot fail for a number literal
	return &id
}
