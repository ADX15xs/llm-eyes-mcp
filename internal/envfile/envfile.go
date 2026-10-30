// Package envfile parses the KEY=VALUE dotenv format used by launcher shells
// and dev harnesses. The server itself never reads .env (see .env.example) —
// this package exists for the harnesses in cmd/ that stand in for the launcher.
package envfile

import (
	"bufio"
	"os"
	"strings"
)

// Load reads the given dotenv files in order and merges them into one map;
// later files win. Missing files are skipped so callers can list candidates
// freely. Comment lines (#) and lines without "=" are ignored.
func Load(paths ...string) map[string]string {
	values := make(map[string]string)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if key != "" {
				values[key] = value
			}
		}
		f.Close()
	}
	return values
}
