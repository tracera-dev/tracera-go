package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// AttachOutOfScopeReason is the shared runner skip message for filtered bounds.
const AttachOutOfScopeReason = "Tracera: case not in attach scope"

// AttachScopeSkip is panicked by [TestCase] when no skip handler is installed.
// Runners should register [SetAttachScopeSkipHandler] so out-of-scope becomes a
// native skip instead of a panic.
type AttachScopeSkip struct{}

func (AttachScopeSkip) Error() string { return AttachOutOfScopeReason }

// AttachScope is the wire / debug allow-list from GET …/autotest/attach-scope.
type AttachScope struct {
	RootTestCaseIDs []int
}

var (
	allowedRoots             map[int]struct{}
	executionSkipLogged      = map[int]struct{}{}
	executionSkipLoggedMu    sync.Mutex
	attachScopeSkipHandler   func(reason string)
	attachScopeSkipHandlerMu sync.Mutex
)

// SetAttachScopeSkipHandler installs the runner skip callback used by [TestCase]
// when a bound id is out of attach scope (Dart/Python parity). Pass nil to clear.
func SetAttachScopeSkipHandler(fn func(reason string)) {
	attachScopeSkipHandlerMu.Lock()
	attachScopeSkipHandler = fn
	attachScopeSkipHandlerMu.Unlock()
}

// ResetAttachFilterForTests clears process filter state.
func ResetAttachFilterForTests() {
	allowedRoots = nil
	executionSkipLoggedMu.Lock()
	executionSkipLogged = map[int]struct{}{}
	executionSkipLoggedMu.Unlock()
	SetAttachScopeSkipHandler(nil)
}

// SetAttachFilter installs or clears the allow-list.
func SetAttachFilter(scope *AttachScope) {
	if scope != nil {
		allowedRoots = make(map[int]struct{}, len(scope.RootTestCaseIDs))
		for _, id := range scope.RootTestCaseIDs {
			allowedRoots[id] = struct{}{}
		}
	} else {
		allowedRoots = nil
	}
	executionSkipLoggedMu.Lock()
	executionSkipLogged = map[int]struct{}{}
	executionSkipLoggedMu.Unlock()
}

// IsAllowed reports whether a bound case may run and report.
// Unbound (id <= 0) is always allowed. Nil filter allows every bound id.
func IsAllowed(testCaseID int) bool {
	if testCaseID <= 0 {
		return true
	}
	if allowedRoots == nil {
		return true
	}
	_, ok := allowedRoots[testCaseID]
	return ok
}

// LogAttachSkipExecution emits debug when a runner skips execution.
func LogAttachSkipExecution(testCaseID int) {
	executionSkipLoggedMu.Lock()
	executionSkipLogged[testCaseID] = struct{}{}
	executionSkipLoggedMu.Unlock()
	LogDebug("Tracera: skip execution — case %d not in attach scope", testCaseID)
	// Drop id + name so enqueue ShouldReport is false (no Skipped/Unknown card).
	clearBindForAttachAbort()
}

// clearBindForAttachAbort unbinds the active pin after an out-of-scope skip so a
// prior in-scope id or runner-seeded name cannot still upload.
func clearBindForAttachAbort() {
	ctx := GetContext()
	if ctx == nil {
		return
	}
	ctx.SetTestCaseID(0)
	ctx.SetAutotestName("")
}

// LogAttachSkipReport emits debug on the report belt; suppressed after execution-skip.
func LogAttachSkipReport(testCaseID int, label string) {
	executionSkipLoggedMu.Lock()
	_, had := executionSkipLogged[testCaseID]
	if had {
		delete(executionSkipLogged, testCaseID)
	}
	executionSkipLoggedMu.Unlock()
	if had {
		return
	}
	name := strings.TrimSpace(label)
	if name == "" {
		name = "bound"
	}
	LogDebug("Tracera: skip report — case %d not in attach scope (%s)", testCaseID, name)
}

// ShouldReport is true for bound ids in the allow-list, or unbound with a name.
func ShouldReport(testCaseID int, autotestName string) bool {
	if testCaseID > 0 {
		return IsAllowed(testCaseID)
	}
	return strings.TrimSpace(autotestName) != ""
}

// ParseAttachScope builds a scope from decoded root ids.
func ParseAttachScope(roots []int) *AttachScope {
	if roots == nil {
		roots = []int{}
	}
	return &AttachScope{RootTestCaseIDs: roots}
}

// ParseAttachScopeJSON parses wire JSON; non-object (null / array / scalar) → nil
// (fail-open, do not filter). Empty object → empty allow-list.
func ParseAttachScopeJSON(data []byte) *AttachScope {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var wire struct {
		RootTestCaseIDs []int `json:"rootTestCaseIds"`
	}
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return nil
	}
	return ParseAttachScope(wire.RootTestCaseIDs)
}

func cachePath(projectID, testRunID int) string {
	return filepath.Join(
		os.TempDir(),
		"tracera-attach-scope",
		"p"+strconv.Itoa(projectID)+"-r"+strconv.Itoa(testRunID)+".json",
	)
}

// ReadAttachScopeCache reads shared worker cache; nil on miss / corrupt / non-object.
func ReadAttachScopeCache(projectID, testRunID int) *AttachScope {
	raw, err := os.ReadFile(cachePath(projectID, testRunID))
	if err != nil {
		return nil
	}
	return ParseAttachScopeJSON(raw)
}

// WriteAttachScopeCache persists scope for peer / worker processes (best-effort, atomic).
func WriteAttachScopeCache(projectID, testRunID int, scope *AttachScope) {
	if scope == nil {
		return
	}
	dir := filepath.Join(os.TempDir(), "tracera-attach-scope")
	_ = os.MkdirAll(dir, 0o755)
	payload, err := json.Marshal(map[string]any{
		"rootTestCaseIds": scope.RootTestCaseIDs,
	})
	if err != nil {
		return
	}
	target := cachePath(projectID, testRunID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, target)
}
