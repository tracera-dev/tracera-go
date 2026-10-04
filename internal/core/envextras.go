package core

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

func osLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

// goVersion reports the toolchain that built the test binary, without the
// leading "go" so the Env tab reads like the other languages.
func goVersion() string {
	return strings.TrimPrefix(runtime.Version(), "go")
}

// ModuleVersion resolves a dependency version from the build info. It returns
// "" when the module is absent so a runner never reports the adapter version
// under a framework key.
func ModuleVersion(modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			return strings.TrimPrefix(dep.Version, "v")
		}
	}
	return ""
}

// EnvVarsExtraOptions describes the synthetic Env keys a runner contributes.
type EnvVarsExtraOptions struct {
	Retry      int
	RetryKey   string
	Version    string
	VersionKey string
	Extra      map[string]string
}

// BuildEnvVarsExtra returns the synthetic Env keys (OS / ARCH / GO_VERSION)
// plus runner version and retry keys when supplied.
func BuildEnvVarsExtra(opts EnvVarsExtraOptions) map[string]string {
	out := map[string]string{
		"OS":         osLabel(),
		"ARCH":       runtime.GOARCH,
		"GO_VERSION": goVersion(),
	}
	if opts.VersionKey != "" && opts.Version != "" {
		out[opts.VersionKey] = opts.Version
	}
	if opts.RetryKey != "" && opts.Retry > 0 {
		out[opts.RetryKey] = strconv.Itoa(opts.Retry)
	}
	for k, v := range opts.Extra {
		out[k] = v
	}
	return out
}
