package vlm

import (
	"context"
	"errors"
	"testing"
)

// mock_test exercises the injected Provider used by the smoke tests and the
// --selftest path. Call injection lets tests drive tools without the network.

func TestNewMockRepliesWithInjectedText(t *testing.T) {
	m := NewMock("glm", "hello world")
	resp, err := m.Complete(context.Background(), Request{Image: []byte("x"), ImageMIME: "image/png"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("mock text = %q, want hello world", resp.Text)
	}
	if resp.Model != "glm-mock" {
		t.Errorf("mock model = %q, want glm-mock", resp.Model)
	}
	if m.Name() != "glm" {
		t.Errorf("mock name = %q, want glm", m.Name())
	}
}

func TestNewMockRecordsCalls(t *testing.T) {
	m := NewMock("glm", "ok")
	for i := 0; i < 3; i++ {
		if _, err := m.Complete(context.Background(), Request{Prompt: "p"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if m.CallCount() != 3 {
		t.Errorf("CallCount = %d, want 3", m.CallCount())
	}
	calls := m.Calls()
	if len(calls) != 3 || calls[0].Prompt != "p" {
		t.Errorf("Calls() should return every recorded request: %+v", calls)
	}
}

func TestNewFailingMockAlwaysErrors(t *testing.T) {
	m := NewFailingMock("bad", "boom")
	if _, err := m.Complete(context.Background(), Request{}); err == nil {
		t.Errorf("failing mock must return an error")
	}
	if m.CallCount() != 1 {
		t.Errorf("failing mock should still record the call: count=%d", m.CallCount())
	}
}

func TestMockCustomCompleteFn(t *testing.T) {
	m := NewMock("glm", "ignored")
	m.CompleteFn = func(ctx context.Context, req Request) (*Response, error) {
		if req.ImageMIME == "" {
			return nil, errors.New("missing mime")
		}
		return &Response{Text: "custom-" + req.Prompt}, nil
	}
	resp, err := m.Complete(context.Background(), Request{Prompt: "q", Image: []byte("x"), ImageMIME: "image/png"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "custom-q" {
		t.Errorf("custom CompleteFn not used: %q", resp.Text)
	}
	if _, err := m.Complete(context.Background(), Request{Prompt: "q"}); err == nil {
		t.Errorf("custom CompleteFn error path not honoured")
	}
}

func TestMockModelVersionFallback(t *testing.T) {
	m := &Mock{ProviderName: "x"} // Version empty -> name+"-mock"
	if m.ModelVersion() != "x-mock" {
		t.Errorf("ModelVersion fallback = %q, want x-mock", m.ModelVersion())
	}
	m.Version = "v9"
	if m.ModelVersion() != "v9" {
		t.Errorf("ModelVersion should use Version when set: %q", m.ModelVersion())
	}
}
