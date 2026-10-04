package core

import (
	"regexp"
	"strings"
)

var secretKeyRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|authorization|private[_-]?key|credential|access[_-]?key)`)

var allowExact = map[string]bool{
	"CI": true, "BROWSER": true, "TEST_ENV": true, "RUNNER_OS": true, "RUNNER_ARCH": true,
}

var allowPrefix = []string{
	"GITHUB_", "GITLAB_", "RUNNER_", "CIRCLE_", "BUILDKITE_", "GINKGO_", "GODOG_",
}

// Harness noise the GINKGO_ / GODOG_ prefixes would otherwise let through. The
// snapshot is allowlist-first, so keys no prefix admits (GOPATH, GOFLAGS,
// NODE_ENV, …) need no entry here.
var denyExact = map[string]bool{
	"GINKGO_EDITOR_INTEGRATION": true,
	"GINKGO_PARALLEL_PROTOCOL":  true,
	"GINKGO_PRESERVE_CACHE":     true,
	"GINKGO_PRUNE_STACK":        true,
	"GINKGO_TIME_FORMAT":        true,
	"GODOG_FEATURES":            true,
	"GODOG_SEED":                true,
	"GODOG_TESTED_PACKAGE":      true,
}

// SnapshotEnvVars collects a safe CI/runner subset for TestResult.envVars.
// Pass nil to read the process environment.
func SnapshotEnvVars(env map[string]string) map[string]string {
	if env == nil {
		env = EnvMap()
	}
	out := make(map[string]string)
	for rawKey, value := range env {
		if value == "" {
			continue
		}
		key := strings.TrimSpace(rawKey)
		if key == "" || len(key) > 128 || len(value) > 4096 {
			continue
		}
		if secretKeyRe.MatchString(key) || strings.HasPrefix(key, "TRACERA_") || denyExact[key] {
			continue
		}
		allowed := allowExact[key]
		if !allowed {
			for _, prefix := range allowPrefix {
				if strings.HasPrefix(key, prefix) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			continue
		}
		out[key] = value
		if len(out) >= 100 {
			break
		}
	}
	return out
}
