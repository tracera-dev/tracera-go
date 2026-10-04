package core

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	csiRe       = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	oscRe       = regexp.MustCompile("\x1b\\][^\x07]*(?:\x07|\x1b\\\\)")
	skipWrapper = regexp.MustCompile(`(?i)^(?:skipped)\s*:\s*`)
)

const maxTextChars = 65536

// StripANSI removes CSI / OSC escape sequences from runner output.
func StripANSI(text string) string {
	return csiRe.ReplaceAllString(oscRe.ReplaceAllString(text, ""), "")
}

// JoinMessageStack joins a message and a stack without duplicating the first
// line when the stack already repeats the message.
func JoinMessageStack(message, stack string) string {
	m := strings.TrimSpace(message)
	s := strings.TrimSpace(stack)
	switch {
	case m == "" && s == "":
		return ""
	case s == "":
		return m
	case m == "":
		return s
	case s == m || strings.HasPrefix(s, m) || strings.Contains(s, m):
		return s
	default:
		return m + "\n" + s
	}
}

// Failure carries a message plus an optional captured stack (panic recovery).
type Failure struct {
	Message string
	Stack   string
}

func (f Failure) Error() string { return f.Message }

// FormatFailureText renders a readable message plus stack when available.
// Strings, errors, Failure values and panic values are all accepted.
func FormatFailureText(err any) string {
	switch v := err.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(StripANSI(v))
	case Failure:
		return strings.TrimSpace(StripANSI(JoinMessageStack(v.Message, v.Stack)))
	case *Failure:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(StripANSI(JoinMessageStack(v.Message, v.Stack)))
	case error:
		return strings.TrimSpace(StripANSI(v.Error()))
	default:
		return strings.TrimSpace(StripANSI(fmt.Sprintf("%v", v)))
	}
}

// NormalizeSkipReason strips runner wrappers so the comment is the customer
// reason only.
func NormalizeSkipReason(reason string) string {
	text := strings.TrimSpace(reason)
	if text == "" {
		return ""
	}
	return strings.TrimSpace(skipWrapper.ReplaceAllString(text, ""))
}

// AppendText appends next to prev, skipping duplicates and capping length.
func AppendText(prev, next string) string {
	t := strings.TrimSpace(next)
	if t == "" {
		return prev
	}
	if prev == "" {
		return t
	}
	if strings.HasSuffix(prev, t) {
		return prev
	}
	parts := strings.Split(prev, "\n\n")
	if parts[len(parts)-1] == t {
		return prev
	}
	joined := prev + "\n\n" + t
	if len(joined) <= maxTextChars {
		return joined
	}
	// Cut on a rune boundary — byte slicing would leave a broken character.
	runes := []rune(joined)
	if len(runes) <= maxTextChars {
		return joined
	}
	return string(runes[:maxTextChars-1]) + "…"
}
