// Package godog reports Godog suites to Tracera.
//
// Bind a Scenario with a `@tracera.id:‹id›` tag and wire the adapter from the
// suite and scenario initializers; Gherkin steps are reported automatically
// and the stock Before / After scenario hooks become Setup and Teardown.
//
//	godog.TestSuite{
//		TestSuiteInitializer: traceragodog.RegisterSuite,
//		ScenarioInitializer: traceragodog.Initialize(func(sc *godog.ScenarioContext) {
//			// Before / After / step definitions …
//		}),
//	}
//
// Customer how-to: https://tracera.dev/docs/adapters
package godog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"github.com/tracera-dev/tracera-go/plugin"
)

const godogModulePath = "github.com/cucumber/godog"

// godogVersion is the Godog version for the Env tab: the build info when the
// binary carries it, else Godog's own version when it is a real release. It
// stays empty rather than reporting a placeholder.
func godogVersion() string {
	if version := plugin.ModuleVersion(godogModulePath); version != "" {
		return version
	}
	if version := strings.TrimPrefix(godog.Version, "v"); version != "" && version != "0.0.0-dev" {
		return version
	}
	return ""
}

var (
	reporter  = plugin.CreateReporterState()
	startOnce sync.Once
	endOnce   sync.Once

	// active keys the live scenarios by pickle id so the Before / After
	// scenario hooks never share state — `godog.Options.Concurrency` runs
	// several scenarios at once, each on its own goroutine.
	active sync.Map // scenario id -> *scenarioState
)

// scenarioState is owned by the goroutine running its scenario: Godog calls
// the scenario hooks, the step hooks and the step bodies of one Scenario in
// sequence on that goroutine.
type scenarioState struct {
	ctx      *plugin.TestContext
	started  time.Time
	stepOpen *plugin.StepHandle
}

// stateKey carries the live scenario state through the context Godog threads
// from the Before-scenario hook into every step hook.
type stateKey struct{}

func stateFrom(ctx context.Context) *scenarioState {
	state, _ := ctx.Value(stateKey{}).(*scenarioState)
	return state
}

// ensureStarted opens the reporting session on first use.
func ensureStarted() {
	startOnce.Do(func() { plugin.BeginReporting(reporter) })
}

// RegisterSuite wires the reporting lifecycle onto a Godog TestSuiteContext.
//
//	godog.TestSuite{TestSuiteInitializer: traceragodog.RegisterSuite, …}
func RegisterSuite(tsc *godog.TestSuiteContext) {
	tsc.BeforeSuite(func() { ensureStarted() })
	tsc.AfterSuite(func() { endOnce.Do(func() { plugin.EndReporting(reporter) }) })
}

// Initialize returns a ScenarioInitializer that wires Tracera, runs fn, then
// closes each Scenario after every customer After hook. Prefer this over
// Register so stock After-scenario teardown still lands on the card:
//
//	ScenarioInitializer: traceragodog.Initialize(func(sc *godog.ScenarioContext) {
//		sc.Before(setup)
//		sc.After(teardown)
//		// step definitions …
//	}),
func Initialize(fn func(*godog.ScenarioContext)) func(*godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		registerLifecycle(sc)
		if fn != nil {
			fn(sc)
		}
		sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
			endScenario(scenario, err)
			return ctx, nil
		})
	}
}

// Register wires scenario and step hooks onto a Godog ScenarioContext, and
// closes the result in an After hook registered at this call. Prefer
// Initialize: Godog runs After hooks in registration order, so a later
// customer After that reports teardown steps would otherwise run after the
// result is already closed. Keep Register only when you register those After
// hooks *before* this call.
func Register(sc *godog.ScenarioContext) {
	registerLifecycle(sc)
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		endScenario(scenario, err)
		return ctx, nil
	})
}

// registerLifecycle installs Before + step hooks without closing the result.
func registerLifecycle(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, stateKey{}, beginScenario(scenario)), nil
	})

	sc.StepContext().Before(func(ctx context.Context, st *godog.Step) (context.Context, error) {
		if state := stateFrom(ctx); state != nil {
			plugin.SetPhase(plugin.SectionTest)
			state.stepOpen = plugin.BeginStep(st.Text)
		}
		return ctx, nil
	})
	sc.StepContext().After(func(ctx context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
		if state := stateFrom(ctx); state != nil {
			closeStep(state, status, err)
		}
		// Whatever runs after the last Gherkin step is teardown; the next
		// step's Before hook moves the section back to Test.
		plugin.SetPhase(plugin.SectionTeardown)
		return ctx, nil
	})
}

func scenarioTags(scenario *godog.Scenario) []string {
	out := make([]string, 0, len(scenario.Tags))
	for _, tag := range scenario.Tags {
		out = append(out, tag.Name)
	}
	return out
}

func beginScenario(scenario *godog.Scenario) *scenarioState {
	ensureStarted()

	testCaseID, bindName := plugin.ParseBindTags(scenarioTags(scenario))
	name := bindName
	if name == "" {
		name = scenario.Name
	}
	ctx := plugin.CreateTestContext(testCaseID, name)
	state := &scenarioState{ctx: ctx, started: time.Now()}
	active.Store(scenario.Id, state)

	// Godog runs one Scenario per goroutine — hooks, step hooks and step
	// bodies included — so the pin is per goroutine and concurrent scenarios
	// stay isolated.
	plugin.InstallContext(ctx)
	// Everything before the first Gherkin step — the stock Before-scenario
	// hooks — belongs to Setup.
	plugin.SetPhase(plugin.SectionSetup)
	return state
}

// closeStep finishes the Tracera step for one Gherkin step, following the
// status Godog resolved rather than assuming the step ran.
func closeStep(state *scenarioState, status godog.StepResultStatus, err error) {
	handle := state.stepOpen
	state.stepOpen = nil
	if handle == nil {
		return
	}
	switch status {
	case godog.StepSkipped, godog.StepUndefined, godog.StepPending:
		// Godog never ran the body — the step must not show up in Steps.
		handle.Discard()
	case godog.StepFailed, godog.StepAmbiguous:
		if isBeforeHookFailure(err) {
			// A failed Before-scenario hook reaches the first Gherkin step, but
			// the step never ran: the hook error belongs to the result.
			handle.Discard()
			return
		}
		handle.Fail(failureOf(err))
		handle.Close()
	default:
		handle.Close()
	}
}

// beforeHookFailurePrefix is what Godog puts in front of an error returned by
// a Before-scenario hook (a panicking hook carries no prefix and never opens
// a step at all).
const beforeHookFailurePrefix = "before scenario hook failed"

func isBeforeHookFailure(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), beforeHookFailurePrefix)
}

// failureOf keeps the stack Godog captured when it recovered a panic (the
// `%+v` form of its error). A plain error returned from a hook or a step
// carries no stack and stays message-only.
func failureOf(err error) plugin.Failure {
	failure := plugin.Failure{Message: err.Error()}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if _, ok := e.(fmt.Formatter); !ok {
			continue
		}
		detailed, message := fmt.Sprintf("%+v", e), e.Error()
		if len(detailed) > len(message) && strings.HasPrefix(detailed, message) {
			failure.Stack = strings.TrimSpace(detailed[len(message):])
			break
		}
	}
	return failure
}

// isSkip reports whether Godog ended the Scenario without a failure: a step
// returned godog.ErrSkip, or a step was pending / undefined outside strict
// mode.
func isSkip(err error) bool {
	return errors.Is(err, godog.ErrSkip) ||
		errors.Is(err, godog.ErrPending) ||
		errors.Is(err, godog.ErrUndefined)
}

// skipReason drops Godog's bare sentinel text; only a wrapped reason —
// `fmt.Errorf("needs a seeded account: %w", godog.ErrSkip)` — is the
// customer's own. `Error()` of a `%w` wrap is `"reason: skipped"` — strip
// the sentinel suffix so the comment matches other Gherkin runners.
func skipReason(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, sentinel := range []error{godog.ErrSkip, godog.ErrPending, godog.ErrUndefined} {
		if text == sentinel.Error() {
			return ""
		}
		if !errors.Is(err, sentinel) {
			continue
		}
		suffix := ": " + sentinel.Error()
		if strings.HasSuffix(text, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(text, suffix))
		}
	}
	return text
}

func endScenario(scenario *godog.Scenario, scenarioErr error) {
	value, _ := active.LoadAndDelete(scenario.Id)
	state, _ := value.(*scenarioState)

	defer plugin.ClearContext()
	if state == nil {
		return
	}

	ctx := state.ctx
	payload := plugin.SnapshotPayload(ctx)
	status := "Passed"
	switch {
	case isSkip(scenarioErr):
		status = "Skipped"
		plugin.MarkSkipped(ctx, skipReason(scenarioErr))
		payload = plugin.SnapshotPayload(ctx)
	case scenarioErr != nil:
		status = "Failed"
		if !plugin.PayloadHasStepError(payload) {
			plugin.RecordFailure(ctx, failureOf(scenarioErr))
			payload = plugin.SnapshotPayload(ctx)
		}
	case plugin.PayloadHasFailedStep(payload) || ctx.ResultError() != "":
		status = "Failed"
	}

	durationMinutes := plugin.DurationMinutesFromMs(float64(time.Since(state.started).Milliseconds()))
	plugin.EnqueueResult(reporter, &plugin.PendingResult{
		Ctx:             ctx,
		Status:          status,
		DurationMinutes: &durationMinutes,
		Payload:         &payload,
		EnvVarsExtra: plugin.BuildEnvVarsExtra(plugin.EnvVarsExtraOptions{
			VersionKey: "GODOG_VERSION",
			Version:    godogVersion(),
		}),
	})
}
