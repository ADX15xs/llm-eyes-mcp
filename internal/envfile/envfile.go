// Package envfile parses the KEY=VALUE dotenv format used by launcher shells
// and dev harnesses. The server itself never reads .env (see .env.example) —
// this package exists for the harnesses in cmd/ that stand in for the launcher.
package envfile

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// Load reads the given dotenv files in order and merges them into one map;
// later files win. Files that are missing or fail to be read are skipped so
// callers can list candidates freely — a partially read file contributes
// nothing rather than a truncated guess. Comment lines (#) and lines without
// "=" are ignored.
func Load(paths ...string) map[string]string {
	values := make(map[string]string)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if vars := parse(f); vars != nil {
			for k, v := range vars {
				values[k] = v
			}
		}
		f.Close()
	}
	return values
}

// parse returns nil when the input cannot be read to EOF.
func parse(r io.Reader) map[string]string {
	vars := make(map[string]string)
	sc := bufio.NewScanner(r)
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
			vars[key] = value
		}
	}
	if sc.Err() != nil {
		return nil
	}
	return vars
}
