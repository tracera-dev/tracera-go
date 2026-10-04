package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const dotenvMaxDepth = 64

var dotenvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isTraceraEnvKey(key string) bool {
	return strings.HasPrefix(key, "TRACERA_")
}

// EnvMap snapshots the process environment as a map.
func EnvMap() map[string]string {
	out := make(map[string]string)
	for _, entry := range os.Environ() {
		eq := strings.Index(entry, "=")
		if eq <= 0 {
			continue
		}
		out[entry[:eq]] = entry[eq+1:]
	}
	return out
}

func parseDotenv(text string) map[string]string {
	out := make(map[string]string)
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		body := line
		if rest, ok := strings.CutPrefix(body, "export "); ok {
			body = strings.TrimSpace(rest)
		}
		eq := strings.Index(body, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(body[:eq])
		if !dotenvKeyRe.MatchString(key) {
			continue
		}
		value := strings.TrimSpace(body[eq+1:])
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		out[key] = value
	}
	return out
}

// FindDotenvPath returns the nearest `.env` walking up from startDir.
//
// The stop directory itself (home by default) and the filesystem root are
// skipped so a stray `~/.env` never leaks into a test run.
func FindDotenvPath(startDir, stopAt string) string {
	if stopAt == "" {
		if home, err := os.UserHomeDir(); err == nil {
			stopAt = home
		}
	}
	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		startDir = cwd
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	stop, _ := filepath.Abs(stopAt)
	for i := 0; i < dotenvMaxDepth; i++ {
		if stop != "" && dir == stop {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		candidate := filepath.Join(dir, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		dir = parent
	}
	return ""
}

func readDotenvFile(cwd, stopAt string) map[string]string {
	path := FindDotenvPath(cwd, stopAt)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseDotenv(string(raw))
}

// MergeEnvWithDotenv fills missing / empty TRACERA_* keys from the nearest `.env`.
// Other keys in the file are ignored. Process env always wins; os.Environ is never mutated.
func MergeEnvWithDotenv(env map[string]string, cwd string) map[string]string {
	return MergeEnvWithDotenvStopAt(env, cwd, "")
}

// MergeEnvWithDotenvStopAt is MergeEnvWithDotenv with an explicit walk-up stop
// directory (tests pin it so the developer home `.env` is irrelevant).
func MergeEnvWithDotenvStopAt(env map[string]string, cwd, stopAt string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	for k, v := range readDotenvFile(cwd, stopAt) {
		if !isTraceraEnvKey(k) {
			continue
		}
		if out[k] == "" {
			out[k] = v
		}
	}
	return out
}
