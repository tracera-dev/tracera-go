// Package gotest reports stdlib `testing` runs to Tracera.
//
// Wire the package once from TestMain and bind each test to a Tracera test
// case; steps, comments, errors and attachments come from the root `tracera`
// package. Prefer gotest.Skip / gotest.Skipf so the reason becomes the result
// comment (stock t.Skip alone does not expose the message to reporters).
//
//	func TestMain(m *testing.M) { os.Exit(gotest.Run(m)) }
//
//	func TestLogin(t *testing.T) {
//		gotest.TestCase(t, 101)
//		tracera.Step("open login", func() { /* ... */ })
//	}
//
// Customer how-to: https://tracera.dev/docs/adapters
package gotest

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tracera "github.com/tracera-dev/tracera-go"
	"github.com/tracera-dev/tracera-go/plugin"
)

var (
	reporter  = plugin.CreateReporterState()
	startOnce sync.Once
	endOnce   sync.Once

	// wired is set once Run drives the process: only then does something close
	// the run and flush what is still buffered when the binary exits.
	wired      atomic.Bool
	unwiredLog sync.Once
)

// ensureStarted opens the reporting session on first use. Run calls it; tests
// that bind without a TestMain get it lazily.
func ensureStarted() {
	startOnce.Do(func() { plugin.BeginReporting(reporter) })
}

// Run wires reporting around the stdlib test runner and returns the exit code.
//
//	func TestMain(m *testing.M) { os.Exit(gotest.Run(m)) }
func Run(m *testing.M) int {
	wired.Store(true)
	ensureStarted()
	code := m.Run()
	finish()
	return code
}

func finish() {
	endOnce.Do(func() { plugin.EndReporting(reporter) })
}

type testState struct {
	ctx      *plugin.TestContext
	started  time.Time
	bindSite string
}

var active sync.Map // *testing.T -> *testState

// TestCase binds t to a Tracera test case id, with an optional result name
// (the Go test name is used when omitted). Call it at the top of the test; the
// result is reported from a t.Cleanup once every other cleanup finished.
//
// Call t.Parallel() *before* TestCase: the reported duration starts counting
// here, and a later t.Parallel() blocks until the parent test returns, which
// would be billed to the test.
//
// Calling it twice on the same t rebinds without starting a second result.
func TestCase(t *testing.T, testCaseID int, name ...string) {
	t.Helper()
	ensureStarted()
	warnIfUnwired()

	if existing, ok := active.Load(t); ok {
		state := existing.(*testState)
		state.ctx.SetTestCaseID(testCaseID)
		if len(name) > 0 && name[0] != "" {
			state.ctx.SetAutotestName(name[0])
		}
		return
	}

	autotestName := t.Name()
	if len(name) > 0 && name[0] != "" {
		autotestName = name[0]
	}
	ctx := plugin.CreateTestContext(testCaseID, autotestName)
	state := &testState{ctx: ctx, started: time.Now(), bindSite: callerSite(2)}
	active.Store(t, state)
	plugin.InstallContext(ctx)
	plugin.SetPhase(plugin.SectionTest)

	t.Cleanup(func() { report(t, state) })
}

// Skip records the reason as a result comment (when non-empty) and then calls
// t.Skip. Prefer this over stock t.Skip: the standard library does not hand the
// skip message to reporters.
func Skip(t *testing.T, args ...any) {
	t.Helper()
	recordSkipReason(fmt.Sprintln(args...))
	t.Skip(args...)
}

// Skipf is like Skip with a format string.
func Skipf(t *testing.T, format string, args ...any) {
	t.Helper()
	recordSkipReason(fmt.Sprintf(format, args...))
	t.Skipf(format, args...)
}

func recordSkipReason(raw string) {
	if text := strings.TrimSpace(raw); text != "" {
		tracera.Comment(text)
	}
}

// warnIfUnwired tells the customer, once, that TestMain does not call Run: the
// stdlib has no process-exit hook, so without it buffered results can be lost
// and the run is never closed.
func warnIfUnwired() {
	if wired.Load() {
		return
	}
	unwiredLog.Do(func() {
		plugin.LogWarn("Tracera: gotest.Run(m) is not wired in TestMain — results may not be uploaded " +
			"and the run stays open. Add: func TestMain(m *testing.M) { os.Exit(gotest.Run(m)) }")
	})
}

// callerSite renders `file:line` skip frames up the stack ("" when unknown).
func callerSite(skip int) string {
	if _, file, line, ok := runtime.Caller(skip); ok {
		return fmt.Sprintf("%s:%d", file, line)
	}
	return ""
}

// testFileFrames returns the `_test.go` locations still on the stack, nearest
// first. A t.Fatalf unwinds through runtime.Goexit before cleanups run, so the
// slice is often empty and the bind site is the only location left.
func testFileFrames() []string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.File, "_test.go") {
			out = append(out, fmt.Sprintf("%s:%d", frame.File, frame.Line))
		}
		if !more {
			return out
		}
	}
}

// defaultFailure is the fail text for a test the stdlib marked failed without
// Tracera seeing the failure. `testing` keeps the t.Error / t.Fatal message to
// itself, so the result names the test and points at the source locations we
// can still resolve — tracera.Error or a failing tracera.Step carries the real
// assertion text.
func defaultFailure(testName, bindSite string) plugin.Failure {
	seen := make(map[string]bool, 4)
	var lines []string
	add := func(site, note string) {
		if site == "" || seen[site] {
			return
		}
		seen[site] = true
		lines = append(lines, "\t"+site+note)
	}
	add(bindSite, " (gotest.TestCase)")
	for _, frame := range testFileFrames() {
		add(frame, "")
	}
	return plugin.Failure{
		Message: fmt.Sprintf(
			"%s failed via t.Error / t.Fatal — the assertion text stays in the go test output",
			testName,
		),
		Stack: strings.Join(lines, "\n"),
	}
}

func report(t *testing.T, state *testState) {
	defer func() {
		active.Delete(t)
		plugin.ClearContext()
	}()

	ctx := state.ctx
	durationMs := float64(time.Since(state.started).Milliseconds())
	payload := plugin.SnapshotPayload(ctx)

	status := "Passed"
	switch {
	case t.Skipped():
		status = "Skipped"
	case t.Failed():
		status = "Failed"
	case plugin.PayloadHasFailedStep(payload) || ctx.ResultError() != "":
		status = "Failed"
	}

	switch {
	case status == "Skipped":
		// Reason is usually already on the result via gotest.Skip / Comment;
		// MarkSkipped still drops steps and clears result-level fail text.
		plugin.MarkSkipped(ctx, "")
		payload = plugin.SnapshotPayload(ctx)
	case status == "Failed" && !plugin.PayloadHasStepError(payload) && ctx.ResultError() == "":
		plugin.RecordFailure(ctx, defaultFailure(t.Name(), state.bindSite))
		payload = plugin.SnapshotPayload(ctx)
	}

	durationMinutes := plugin.DurationMinutesFromMs(durationMs)
	plugin.EnqueueResult(reporter, &plugin.PendingResult{
		Ctx:             ctx,
		Status:          status,
		DurationMinutes: &durationMinutes,
		Payload:         &payload,
		EnvVarsExtra:    plugin.BuildEnvVarsExtra(plugin.EnvVarsExtraOptions{}),
	})
}
