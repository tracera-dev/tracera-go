// Package core holds the Tracera Go adapter implementation.
//
// Customer names are re-exported by the root `tracera` package; runner/plugin
// helpers by `github.com/tracera-dev/tracera-go/plugin`. Nothing here is part
// of the customer surface.
package core

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type logLevel int32

const (
	levelError logLevel = iota
	levelWarn
	levelInfo
	levelDebug
)

const (
	ansiGray    = "\033[90m"
	ansiRed     = "\033[31m"
	ansiYellow  = "\033[33m"
	ansiCyan    = "\033[36m"
	ansiMagenta = "\033[35m"
	ansiGreen   = "\033[32m"
	ansiBold    = "\033[1m"
	ansiReset   = "\033[0m"
)

var currentLevel atomic.Int32

func init() {
	SetLogLevel(os.Getenv("TRACERA_LOG_LEVEL"))
}

func parseLogLevel(raw string) logLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "error":
		return levelError
	case "warn":
		return levelWarn
	case "debug":
		return levelDebug
	default:
		return levelInfo
	}
}

// SetLogLevel applies TRACERA_LOG_LEVEL (error | warn | info | debug).
func SetLogLevel(raw string) {
	currentLevel.Store(int32(parseLogLevel(raw)))
}

func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func paint(f *os.File, color, text string) string {
	if !useColor(f) {
		return text
	}
	return color + text + ansiReset
}

func levelPaint(level logLevel) string {
	switch level {
	case levelError:
		return ansiRed
	case levelWarn:
		return ansiYellow
	case levelDebug:
		return ansiMagenta
	default:
		return ansiCyan
	}
}

func levelLabel(level logLevel) string {
	switch level {
	case levelError:
		return "ERROR"
	case levelWarn:
		return "WARN "
	case levelDebug:
		return "DEBUG"
	default:
		return "INFO "
	}
}

func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

func emit(level logLevel, message string) {
	if int32(level) > currentLevel.Load() {
		return
	}
	stream := os.Stdout
	if level == levelError {
		stream = os.Stderr
	}
	ts := paint(stream, ansiGray, timestamp())
	lines := strings.Split(message, "\n")
	head := fmt.Sprintf("%s %s %s", ts, paint(stream, levelPaint(level), levelLabel(level)), lines[0])
	body := ""
	if len(lines) > 1 {
		body = "\n" + strings.Join(lines[1:], "\n")
	}
	fmt.Fprintln(stream, head+body)
}

// LogError writes an error line to stderr.
func LogError(format string, args ...any) { emit(levelError, fmt.Sprintf(format, args...)) }

// LogWarn writes a warning line to stdout.
func LogWarn(format string, args ...any) { emit(levelWarn, fmt.Sprintf(format, args...)) }

// LogDebug writes a debug line to stdout.
func LogDebug(format string, args ...any) { emit(levelDebug, fmt.Sprintf(format, args...)) }

// LogLink prints the green run link; it ignores the configured level so a
// successful run always shows where the results landed.
func LogLink(url string) {
	stream := os.Stdout
	fmt.Fprintln(stream, paint(stream, ansiGray, timestamp())+" "+paint(stream, ansiBold+ansiGreen, "Tracera: "+url))
}
