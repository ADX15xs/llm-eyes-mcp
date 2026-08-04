// Package mcp implements the Model Context Protocol server over stdio using
// newline-delimited JSON (NDJSON). This package is transport/protocol only: it
// MUST NOT import any business package. Tools are injected through the Tool
// interface so the protocol layer can be tested end-to-end with mocks.
package mcp

import (
	"encoding/json"
)

// ProtocolVersion is the MCP revision this server speaks. Kept as a constant so
// tests can assert on it and upgrades cannot silently drift.
const ProtocolVersion = "2024-11-05"

// JSON-RPC 2.0 error codes used by this server.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ID models the three-state semantics of a JSON-RPC id: present-with-value,
// explicitly null, or absent. A plain int cannot express this.
//
// Unlike the reference implementation in the dev guide (which assumes numeric
// ids), we keep the raw JSON so string ids round-trip unchanged - the spec
// allows both, and echoing back a different type breaks strict clients.
type ID struct {
	raw  json.RawMessage
	seen bool
}

// NewNullID returns an id that marshals to JSON null. Used when a request could
// not be parsed far enough to recover its id.
func NewNullID() *ID { return &ID{} }

// Seen reports whether the id field was present in the incoming message.
func (i *ID) Seen() bool { return i != nil && i.seen }

// String renders the raw id for logging.
func (i *ID) String() string {
	if !i.Seen() {
		return "null"
	}
	return string(i.raw)
}

func (i *ID) UnmarshalJSON(b []byte) error {
	i.raw = append(json.RawMessage(nil), b...)
	i.seen = true
	return nil
}

func (i ID) MarshalJSON() ([]byte, error) {
	if !i.seen || len(i.raw) == 0 {
		return []byte("null"), nil
	}
	return i.raw, nil
}

// Message is an inbound JSON-RPC message. A nil ID means "notification" - the
// server must never emit a response for it.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the message carries no id.
func (m *Message) IsNotification() bool { return m.ID == nil }

// Response is an outbound JSON-RPC response. Per spec the id member is always
// present (null when unrecoverable), so it is deliberately not omitempty.
type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      *ID       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// RPCError is a protocol-level failure. Business failures must NOT use this -
// they go through ToolResult.IsError so the agent can read and recover.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
