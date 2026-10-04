// Package ginkgo reports Ginkgo v2 suites to Tracera.
//
// Call Register() once in the suite file, bind specs with a
// `tracera.id:‹id›` Label, and use the stock BeforeEach / AfterEach hooks —
// they become the Setup and Teardown sections.
//
//	var _ = traceraginkgo.Register()
//
//	var _ = Describe("checkout", Label("tracera.id:101"), func() {
//		It("pays", func() { tracera.Step("pay", func() { /* ... */ }) })
//	})
//
// Customer how-to: https://tracera.dev/docs/adapters
package ginkgo

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/tracera-dev/tracera-go/plugin"
)

// ginkgoVersion is the Ginkgo version for the Env tab. It comes from Ginkgo's
// own constant: `go test` binaries carry no module build info, so a build-info
// lookup would leave the key empty on every customer run.
func ginkgoVersion() string { return types.VERSION }

var (
	reporter  = plugin.CreateReporterState()
	startOnce sync.Once
	endOnce   sync.Once

	// flushPerSpec is set on the parallel processes that never reach
	// ReportAfterSuite; they upload each spec as it finishes instead.
	flushPerSpec atomic.Bool

	mu      sync.Mutex
	current *plugin.TestContext
	started time.Time
)

// ensureStarted opens the reporting session on first use. It runs inside the
// suite rather than while the tree is built, so Ginkgo has already parsed the
// parallel configuration.
func ensureStarted() {
	startOnce.Do(func() {
		config, _ := ginkgo.GinkgoConfiguration()
		if config.ParallelTotal > 1 {
			// `ginkgo -p` forks one process per worker. They share a single run
			// through the file-lock handoff instead of creating N runs, and
			// process 1 — whose ReportAfterSuite runs after every sibling
			// exited — closes it.
			reporter.PeerWorker = fmt.Sprintf("ginkgo-process-%d", config.ParallelProcess)
			reporter.PeerFinisher = config.ParallelProcess == 1
			flushPerSpec.Store(!reporter.PeerFinisher)
		}
		plugin.BeginReporting(reporter)
	})
}

// finishSuite closes the run. Ginkgo runs ReportAfterSuite exactly once — on
// process 1, after every parallel peer finished.
func finishSuite(report ginkgo.Report) {
	if report.PreRunStats.SpecsThatWillRun > 0 {
		// Process 1 may itself have run no specs while the peers reported into
		// the shared run; attach here so that run still gets closed.
		ensureStarted()
	}
	endOnce.Do(func() { plugin.EndReporting(reporter) })
}

// Register wires Tracera into the suite. Call it once in the suite file
// file so the hooks apply to every spec:
//
//	var _ = traceraginkgo.Register()
//
// It returns true so it can sit in a `var _ =` declaration like Ginkgo's own
// tree-building calls.
func Register() bool {
	ginkgo.ReportBeforeEach(func(report ginkgo.SpecReport) { beginSpec(report) })
	ginkgo.BeforeEach(func() { beginAttempt(ginkgo.CurrentSpecReport()) })
	ginkgo.JustBeforeEach(func() { plugin.SetPhase(plugin.SectionTest) })
	// AfterEach nodes unwind innermost first, so a top-level AfterEach would run
	// after the ones inside the customer's containers. JustAfterEach runs right
	// after the It body, ahead of every AfterEach.
	ginkgo.JustAfterEach(func() { plugin.SetPhase(plugin.SectionTeardown) })
	ginkgo.ReportAfterEach(func(report ginkgo.SpecReport) { endSpec(report) })
	ginkgo.ReportAfterSuite("tracera", finishSuite)

	return true
}

func specName(report ginkgo.SpecReport, bindName string) string {
	if bindName != "" {
		return bindName
	}
	if text := report.FullText(); text != "" {
		return text
	}
	return report.LeafNodeText
}

func beginSpec(report ginkgo.SpecReport) {
	ensureStarted()

	testCaseID, bindName := plugin.ParseBindTags(report.Labels())
	ctx := plugin.CreateTestContext(testCaseID, specName(report, bindName))

	mu.Lock()
	current = ctx
	started = time.Now()
	mu.Unlock()

	// Pin in beginAttempt (BeforeEach), not here: suite-before-fail still has
	// `current` for ReportAfterEach when BeforeAll panics before BeforeEach.
}

// beginAttempt opens the Setup section for one attempt. Ginkgo reports a
// retried or repeated spec once (ReportAfterEach runs after the last attempt),
// so what the earlier attempts recorded is dropped here.
//
// Note on Ordered BeforeAll: Ginkgo sorts setup by nesting, so this suite-level
// BeforeEach runs *before* an Ordered BeforeAll. Tracera steps inside a
// successful BeforeAll therefore land in Setup — put suite work in BeforeSuite
// (never pinned) or in BeforeEach. Failing BeforeAll still fans out Failed
// cards (suite_before_fail).
func beginAttempt(report ginkgo.SpecReport) {
	mu.Lock()
	ctx := current
	mu.Unlock()
	if ctx == nil {
		return
	}
	if report.NumAttempts > 1 {
		plugin.ResetForRetry(ctx)
	}
	// Ginkgo runs every node on its own goroutine but only one spec at a time
	// per process (parallel suites fork processes), so the pin is process-wide.
	plugin.InstallProcessContext(ctx)
	plugin.SetPhase(plugin.SectionSetup)
}

// specRetries is the number of retries behind the reported attempt. Repeated
// specs (MustPassRepeatedly) are not retries and report none.
func specRetries(report ginkgo.SpecReport) int {
	if report.MaxFlakeAttempts > 1 && report.NumAttempts > 1 {
		return report.NumAttempts - 1
	}
	return 0
}

func mapState(state types.SpecState) string {
	switch {
	case state.Is(types.SpecStatePassed):
		return "Passed"
	case state.Is(types.SpecStateSkipped | types.SpecStatePending):
		return "Skipped"
	default:
		return "Failed"
	}
}

func failureText(report ginkgo.SpecReport) plugin.Failure {
	failure := report.Failure
	stack := failure.Location.FullStackTrace
	if stack == "" {
		stack = failure.Location.String()
	}
	message := failure.Message
	if failure.ForwardedPanic != "" {
		// A recovered panic reports "Test Panicked"; the panic value is the text
		// the customer needs.
		message = fmt.Sprintf("%s: %s", message, failure.ForwardedPanic)
	}
	return plugin.Failure{Message: message, Stack: stack}
}

func endSpec(report ginkgo.SpecReport) {
	mu.Lock()
	ctx, startedAt := current, started
	current = nil
	mu.Unlock()

	defer plugin.ClearProcessContext()
	if ctx == nil {
		return
	}

	status := mapState(report.State)
	payload := plugin.SnapshotPayload(ctx)
	switch status {
	case "Skipped":
		plugin.MarkSkipped(ctx, report.Failure.Message)
		payload = plugin.SnapshotPayload(ctx)
	case "Failed":
		if !plugin.PayloadHasStepError(payload) {
			plugin.RecordFailure(ctx, failureText(report))
			payload = plugin.SnapshotPayload(ctx)
		}
	default:
		if plugin.PayloadHasFailedStep(payload) || ctx.ResultError() != "" {
			status = "Failed"
		}
	}

	durationMs := float64(report.RunTime.Milliseconds())
	if durationMs <= 0 {
		durationMs = float64(time.Since(startedAt).Milliseconds())
	}
	durationMinutes := plugin.DurationMinutesFromMs(durationMs)
	plugin.EnqueueResult(reporter, &plugin.PendingResult{
		Ctx:             ctx,
		Status:          status,
		DurationMinutes: &durationMinutes,
		Payload:         &payload,
		EnvVarsExtra: plugin.BuildEnvVarsExtra(plugin.EnvVarsExtraOptions{
			VersionKey: "GINKGO_VERSION",
			Version:    ginkgoVersion(),
			RetryKey:   "GINKGO_RETRY",
			Retry:      specRetries(report),
		}),
	})

	if flushPerSpec.Load() {
		plugin.FlushResults(reporter)
	}
}
