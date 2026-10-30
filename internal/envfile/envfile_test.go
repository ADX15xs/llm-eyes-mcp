package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

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
}
