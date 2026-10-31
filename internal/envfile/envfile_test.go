package envfile

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// failingReader hands out one chunk of data, then fails — a stand-in for a
// .env file that dies partway through being read.
type failingReader struct {
	data string
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	return copy(p, r.data), io.ErrUnexpectedEOF
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := write("first.env", "# comment\n\nUOMA_API_KEY=sk-first\nQUOTED=\"value\"\nbroken line\n")
	second := write("second.env", "UOMA_API_KEY=sk-second\n")

	t.Run("merges files with later winning", func(t *testing.T) {
		got := Load(first, second)
		if got["UOMA_API_KEY"] != "sk-second" {
			t.Errorf("UOMA_API_KEY = %q, want sk-second", got["UOMA_API_KEY"])
		}
		if got["QUOTED"] != "value" {
			t.Errorf("QUOTED = %q, want value", got["QUOTED"])
		}
	})

	t.Run("missing files are skipped", func(t *testing.T) {
		got := Load(filepath.Join(dir, "nope.env"), second)
		if got["UOMA_API_KEY"] != "sk-second" {
			t.Errorf("UOMA_API_KEY = %q, want sk-second", got["UOMA_API_KEY"])
		}
	})

	t.Run("partially read files contribute nothing", func(t *testing.T) {
		if got := parse(&failingReader{data: "UOMA_API_KEY=sk-partial\n"}); got != nil {
			t.Errorf("parse = %v, want nil for a truncated input", got)
		}
	})
}
