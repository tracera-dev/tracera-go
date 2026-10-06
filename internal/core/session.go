package core

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// ReporterState is the per-process reporting lifecycle a runner drives.
type ReporterState struct {
	session      *Session
	buffer       *ResultBuffer
	uploadPool   *JobPool
	SoftDisabled bool

	// PeerWorker opts this process into the shared run-id handoff when the
	// runner — not an env var — knows it is one of several peer processes
	// (`ginkgo -p`). Set it before BeginReporting.
	PeerWorker string
	// PeerFinisher marks the one peer process whose end-of-suite hook is
	// guaranteed to run after every sibling exited, so it finishes the shared
	// run instead of waiting for a refcount the others cannot decrement.
	PeerFinisher bool
}

// CreateReporterState builds an idle reporter state.
func CreateReporterState() *ReporterState { return &ReporterState{} }

func failRequired(message string) {
	if !strings.HasPrefix(message, "Tracera:") {
		message = "Tracera: " + message
	}
	LogError("%s", message)
	os.Exit(1)
}

// BeginReporting resolves config, opens or attaches to a run, and arms the
// buffer. Any failure soft-disables reporting unless TRACERA_REQUIRE_REPORT.
func BeginReporting(state *ReporterState) {
	cwd, _ := os.Getwd()
	env := MergeEnvWithDotenv(EnvMap(), cwd)
	SetLogLevel(env["TRACERA_LOG_LEVEL"])
	requireReport := ParseBool(env["TRACERA_REQUIRE_REPORT"], false)

	config, err := LoadConfig(env)
	if err != nil {
		message := "Tracera: " + err.Error()
		if requireReport {
			failRequired(message)
		}
		LogWarn("%s — stopped reporting", message)
		state.SoftDisabled = true
		return
	}

	softDisable := func(err error) {
		if config.RequireReport {
			failRequired(err.Error())
		}
		LogWarn("Tracera: %s — stopped reporting", err)
		state.SoftDisabled = true
	}

	client := NewClient(config)
	app, err := client.GetAppConfig()
	if err != nil {
		softDisable(err)
		return
	}
	if err := client.EnsureCompatible(app); err != nil {
		softDisable(err)
		return
	}

	peerLabel := peerWorkerLabel(state.PeerWorker)
	if peerLabel == "" {
		peerLabel = PeerWorkerLabel(env)
	}

	testRunID := config.TestRunID
	owned := false
	peerHandoff := false
	switch {
	case testRunID > 0:
		// Attaching to a run someone else owns.
	case peerLabel != "":
		LogDebug("Tracera: %s — sharing one test run with its peers", peerLabel)
		id, _, err := ClaimPeerRun(config.ProjectID, config.RunTitle, client.CreateRun)
		if err != nil {
			softDisable(err)
			return
		}
		testRunID = id
		peerHandoff = true
	default:
		id, err := client.CreateRun()
		if err != nil {
			softDisable(err)
			return
		}
		testRunID = id
		owned = true
	}

	state.session = NewSession(client, config, testRunID, owned, peerHandoff)
	state.session.PeerFinisher = peerHandoff && state.PeerFinisher
	state.buffer = NewResultBuffer(state.session)
	state.uploadPool = NewJobPool(config.UploadConcurrency)
	if owned {
		LogDebug("Tracera: created test run #%d", testRunID)
	} else {
		LogDebug("Tracera: using existing test run #%d", testRunID)
	}
}

// EnqueueResult resolves the result's pending attachments and buffers it for
// upload. With an upload pool armed the work runs there, so the test goroutine
// never waits on a blob; EndReporting drains the pool before the final flush.
func EnqueueResult(state *ReporterState, item *PendingResult) {
	if item == nil || item.Ctx == nil {
		return
	}
	if item.Payload == nil {
		payload := SnapshotPayload(item.Ctx)
		item.Payload = &payload
	}
	if state == nil || state.SoftDisabled || state.session == nil || state.buffer == nil ||
		item.Ctx.TestCaseID() <= 0 {
		// Never reported: drop the blob bodies the attachments registered.
		DiscardPendingBlobs(item.Payload)
		return
	}

	session, buffer := state.session, state.buffer
	report := func() {
		if !session.ReportingEnabled() {
			DiscardPendingBlobs(item.Payload)
			return
		}
		if !resolveResultAttachments(session, item.Payload) {
			return
		}
		buffer.Enqueue(item)
	}
	if state.uploadPool == nil {
		report()
		return
	}
	state.uploadPool.Enqueue(report)
}

// resolveResultAttachments uploads the pending attachment slots on payload. It
// reports false when the result must be dropped instead of reported with
// broken attachments (TRACERA_REQUIRE_REPORT).
func resolveResultAttachments(session *Session, payload *Payload) bool {
	errs := ResolvePendingAttachments(session.Client, payload)
	if len(errs) == 0 {
		return true
	}
	session.SetLastError(errs[0])
	if session.Config != nil && session.Config.RequireReport {
		session.NoteFailedReport(1)
		session.NoteUnsentResults(1)
		return false
	}
	return true
}

// FlushResults drains attachment uploads and uploads the buffered results
// without closing the run. Runners whose peer processes have no end-of-suite
// hook call it after each test; it must not overlap a running test.
func FlushResults(state *ReporterState) {
	if state.SoftDisabled || state.session == nil || state.buffer == nil {
		return
	}
	if state.uploadPool != nil {
		state.uploadPool.Drain()
	}
	state.buffer.Flush()
}

// finishSharedRun closes a run this process does not own, with the same error
// accounting as FinishOwnedRun.
func finishSharedRun(session *Session) {
	if err := session.Client.FinishRun(session.TestRunID); err != nil {
		session.SetLastError(err.Error())
		session.NoteFailedReport(1)
		LogError("Tracera: %s", err)
	}
}

// EndReporting drains uploads, flushes results and closes the run.
func EndReporting(state *ReporterState) {
	if state.SoftDisabled || state.session == nil || state.buffer == nil {
		return
	}
	FlushResults(state)
	// Blobs still registered here belong to results nobody reported.
	ClearPendingBlobs()

	session := state.session
	left := state.buffer.PendingCount() + session.UnsentResults()
	switch {
	case left > 0:
		LogError("Tracera: %d result(s) not uploaded — leaving test run open", left)
	case session.PeerHandoff && session.PeerFinisher:
		// This process leaves last by construction, so waiting for the
		// refcount would keep the run open forever — its siblings have no
		// end-of-suite hook to release from.
		DiscardPeerRun(session.Config.ProjectID, session.Config.RunTitle)
		finishSharedRun(session)
	case session.PeerHandoff:
		// Peer sessions are not owned — FinishOwnedRun would no-op.
		ReleasePeerRun(session.Config.ProjectID, session.Config.RunTitle, func() {
			finishSharedRun(session)
		})
	default:
		session.FinishOwnedRun()
	}
	PrintSummary(session)

	if session.Config.RequireReport && session.FailedReport() > 0 {
		failRequired(fmt.Sprintf(
			"Tracera: exiting — %d result(s) could not be reported (TRACERA_REQUIRE_REPORT)",
			session.FailedReport(),
		))
	}
}

// DurationMinutesFromMs converts wall-clock ms to TestResult.durationMinutes:
// one decimal, half-up. Go's math.Round is already half-away-from-zero, but
// the explicit floor(x+0.5) keeps the formula identical across languages
// (Python round and kotlin.math.round are banker's rounding).
func DurationMinutesFromMs(ms float64) float64 {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 {
		return 0
	}
	tenths := (ms / 60_000) * 10
	return math.Floor(tenths+0.5) / 10
}
