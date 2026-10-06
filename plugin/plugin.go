// Package plugin is the runner-facing surface of the Tracera Go core.
//
// The gotest, ginkgo and godog adapters import it to drive the reporting
// lifecycle. It is not part of the customer API and carries no compatibility
// promise for test authors — they use the root `tracera` package.
package plugin

import (
	"github.com/tracera-dev/tracera-go/internal/core"
)

// Reporting lifecycle types.
type (
	// ReporterState is the per-process reporting lifecycle.
	ReporterState = core.ReporterState
	// PendingResult is one buffered TestResult.
	PendingResult = core.PendingResult
	// TestContext is the active test pin.
	TestContext = core.TestContext
	// StepSection is the lifecycle bucket a step lands in.
	StepSection = core.StepSection
	// StepHandle is an open step.
	StepHandle = core.StepHandle
	// Payload is the snapshot of one result.
	Payload = core.Payload
	// Failure carries a message plus a captured stack.
	Failure = core.Failure
	// EnvVarsExtraOptions describes synthetic Env keys.
	EnvVarsExtraOptions = core.EnvVarsExtraOptions
)

// Contract constants shared with the other language adapters.
const (
	// PeerWorkerEnv opts a forked peer process into the shared run-id handoff.
	PeerWorkerEnv = core.PeerWorkerEnv

	SectionSetup    = core.SectionSetup
	SectionTest     = core.SectionTest
	SectionTeardown = core.SectionTeardown
)

// ParseBindTags reads `tracera.id:` / `tracera.name:` from labels or tags.
func ParseBindTags(tags []string) (int, string) { return core.ParseBindTags(tags) }

// Session lifecycle.

// CreateReporterState builds an idle reporter state.
func CreateReporterState() *ReporterState { return core.CreateReporterState() }

// BeginReporting resolves config and opens or attaches to a run.
func BeginReporting(state *ReporterState) { core.BeginReporting(state) }

// FlushResults uploads what is buffered without closing the run. Runners whose
// peer processes have no end-of-suite hook call it after each test.
func FlushResults(state *ReporterState) { core.FlushResults(state) }

// EndReporting drains uploads, flushes results and closes the run.
func EndReporting(state *ReporterState) { core.EndReporting(state) }

// EnqueueResult uploads the result's pending attachments on the upload pool
// and buffers it. Runners report through this rather than the buffer directly.
func EnqueueResult(state *ReporterState, item *PendingResult) { core.EnqueueResult(state, item) }

// Context handling.

// CreateTestContext builds the pin for one test.
func CreateTestContext(testCaseID int, autotestName string) *TestContext {
	return core.CreateTestContext(testCaseID, autotestName)
}

// InstallContext pins ctx on the current goroutine until ClearContext.
func InstallContext(ctx *TestContext) { core.InstallContext(ctx) }

// ClearContext drops the pin for the current goroutine.
func ClearContext() { core.ClearContext() }

// InstallProcessContext pins ctx for goroutines without their own pin. Only
// runners that execute one test at a time per process (Ginkgo nodes, Godog
// steps) may use it.
func InstallProcessContext(ctx *TestContext) { core.InstallProcessContext(ctx) }

// ClearProcessContext drops the process-wide pin.
func ClearProcessContext() { core.ClearProcessContext() }

// GetContext returns the pin for the current goroutine, or nil.
func GetContext() *TestContext { return core.GetContext() }

// SetPhase moves later steps into setup / test / teardown.
func SetPhase(section StepSection) { core.SetPhase(section) }

// BeginStep opens a step the runner closes itself (framework hook wrapping).
func BeginStep(title string) *StepHandle { return core.BeginStep(title) }

// Result shaping.

// SnapshotPayload freezes the step tree and extras for upload.
func SnapshotPayload(ctx *TestContext) Payload { return core.SnapshotPayload(ctx) }

// RecordFailure puts fail text on the open step, else on the result.
func RecordFailure(ctx *TestContext, err any) { core.RecordFailure(ctx, err) }

// MarkSkipped shapes a Skipped result: the reason becomes the result comment,
// recorded steps are dropped and result-level fail text is cleared.
func MarkSkipped(ctx *TestContext, reason string) { core.MarkSkipped(ctx, reason) }

// ResetForRetry drops what a previous attempt recorded (steps and extras) so a
// retried test reports its final attempt only. The bind is kept.
func ResetForRetry(ctx *TestContext) { core.ResetForRetry(ctx) }

// FormatFailureText renders message plus stack when available (no ANSI, no
// duplicated first line).
func FormatFailureText(err any) string { return core.FormatFailureText(err) }

// PayloadHasStepError reports whether any step carries errorMessage (O(1) sticky flag).
func PayloadHasStepError(p Payload) bool { return core.PayloadHasStepError(p) }

// PayloadHasFailedStep reports whether any step finished Failed (O(1) sticky flag).
func PayloadHasFailedStep(p Payload) bool { return core.PayloadHasFailedStep(p) }

// DurationMinutesFromMs converts wall-clock ms to durationMinutes (one
// decimal, half-up).
func DurationMinutesFromMs(ms float64) float64 { return core.DurationMinutesFromMs(ms) }

// Env.

// BuildEnvVarsExtra returns synthetic Env keys plus runner version / retry.
func BuildEnvVarsExtra(opts EnvVarsExtraOptions) map[string]string {
	return core.BuildEnvVarsExtra(opts)
}

// ModuleVersion resolves a dependency version from build info ("" if absent).
func ModuleVersion(modulePath string) string { return core.ModuleVersion(modulePath) }

// Logging (adapter lines are prefixed `Tracera:` by the caller).

// LogWarn writes a warning line.
func LogWarn(format string, args ...any) { core.LogWarn(format, args...) }
