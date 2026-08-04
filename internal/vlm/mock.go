package vlm

import (
	"context"
	"fmt"
	"sync"
)

// Mock is an in-process Provider for tests and for the --selftest smoke run.
// Behaviour is injected through CompleteFn (function-field injection), so tests
// never touch the network.
type Mock struct {
	ProviderName string
	Version      string
	CompleteFn   func(ctx context.Context, req Request) (*Response, error)

	mu    sync.Mutex
	calls []Request
}

// NewMock returns a mock that always replies with the given text.
func NewMock(name, text string) *Mock {
	return &Mock{
		ProviderName: name,
		Version:      name + "-mock",
		CompleteFn: func(context.Context, Request) (*Response, error) {
			return &Response{Text: text, Model: name + "-mock"}, nil
		},
	}
}

// NewFailingMock returns a mock that always fails with the given message.
func NewFailingMock(name, msg string) *Mock {
	return &Mock{
		ProviderName: name,
		Version:      name + "-mock",
		CompleteFn: func(context.Context, Request) (*Response, error) {
			return nil, fmt.Errorf("%s", msg)
		},
	}
}

func (m *Mock) Name() string { return m.ProviderName }

func (m *Mock) ModelVersion() string {
	if m.Version == "" {
		return m.ProviderName + "-mock"
	}
	return m.Version
}

// Complete records the request and delegates to CompleteFn.
func (m *Mock) Complete(ctx context.Context, req Request) (*Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	m.mu.Unlock()
	if m.CompleteFn == nil {
		return &Response{Text: "", Model: m.ModelVersion()}, nil
	}
	return m.CompleteFn(ctx, req)
}

// Calls returns every request the mock received, for assertion.
func (m *Mock) Calls() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Request, len(m.calls))
	copy(out, m.calls)
	return out
}

// CallCount reports how many times Complete was invoked. Cache tests assert on
// this to prove a second identical call did not reach the backend.
func (m *Mock) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}
