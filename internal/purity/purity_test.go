// Package purity is the enforcement point of the repo's language policy
// (AGENTS.md, section "Dev harness & language purity"): file types enter the
// repository through an allowlist, not a banlist. A new language or format is
// therefore rejected by default — either replace it with a Go-native approach
// or amend the allowlist here AND in AGENTS.md with a justification.
package purity

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// allowedExts is the whitelist of file extensions a Go project needs. Every
// entry must carry its justification, mirrored in AGENTS.md.
var allowedExts = map[string]string{
	".go":      "the language of this repo",
	".md":      "documentation (README, AGENTS, docs/)",
	".json":    "spec fixtures and data files (examples/, docs/)",
	".yml":     "CI workflow and config templates",
	".mod":     "go.mod",
	".sum":     "go.sum",
	".png":     "binary acceptance fixture (testdata/compare/)",
	".example": "config/env templates (config.yml.example, .env.example)",
}

// allowedBareNames covers extension-less tracked files.
var allowedBareNames = map[string]bool{
	"Makefile":       true,
	"LICENSE":        true,
	".gitignore":     true,
	".gitattributes": true,
}

// exemptPaths are frozen zones outside the extension policy. They must never
// grow new active material; see AGENTS.md for why each exists.
var exemptPaths = []string{
	"docs/archive/", // frozen probe evidence from the 2026-10-27/28 rounds
}

func TestTrackedFilesFollowLanguagePolicy(t *testing.T) {
	files := trackedFiles(t)
	if len(files) == 0 {
		t.Fatal("git ls-files returned nothing; run this test inside the repository")
	}

	var violations []string
	for _, f := range files {
		if exempted(f) {
			continue
		}
		base := filepath.Base(f)
		ext := strings.ToLower(filepath.Ext(base))
		if ext == base {
			// A pure dotfile (.gitignore): the whole name is the name, so
			// classify it as extension-less rather than as an extension.
			ext = ""
		}
		if ext == "" {
			if allowedBareNames[base] {
				continue
			}
		} else if _, ok := allowedExts[ext]; ok {
			continue
		}
		violations = append(violations, f)
	}
	if len(violations) == 0 {
		return
	}

	shown := violations
	if len(shown) > 10 {
		shown = shown[:10]
	}
	t.Fatalf("%d tracked file(s) violate the language policy (first %d):\n  %s\n"+
		"Purity policy: see AGENTS.md \"Dev harness & language purity\".\n"+
		"Either implement the material in Go, or amend the allowlist there AND in\n"+
		"internal/purity/purity_test.go with a justification for the new type.",
		len(violations), len(shown), strings.Join(shown, "\n  "))
}

func exempted(path string) bool {
	for _, prefix := range exemptPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// trackedFiles returns the files git tracks or has staged, repo-root relative.
// git ls-files without a pathspec is limited to the current directory, so the
// command is pinned to the repo root. Untracked material (node_modules, .env,
// bin/) is not part of the repository and passes; the moment it is `git add`ed
// it gets caught — before the commit.
func trackedFiles(t *testing.T) []string {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	cmd := exec.Command("git", "-c", "core.quotepath=false", "ls-files", "-z")
	cmd.Dir = strings.TrimSpace(string(root))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, filepath.ToSlash(f))
		}
	}
	return files
}
