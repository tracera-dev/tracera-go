package core

import (
	"fmt"
	"sync"
	"time"
)

// ConsecutiveBatchFailLimit is how many transport failures in a row stop
// reporting for the rest of the process.
const ConsecutiveBatchFailLimit = 2

// PendingResult is one buffered TestResult awaiting upload.
type PendingResult struct {
	Ctx             *TestContext
	Status          string
	DurationMinutes *float64
	Payload         *Payload
	EnvVarsExtra    map[string]string

	transportFailures int
}

// Session holds the run this process reports into plus error accounting.
type Session struct {
	Client         *Client
	Config         *Config
	TestRunID      int
	OwnedByAdapter bool
	// PeerHandoff is true when several peer processes share one run via the
	// file-lock handoff; the last one to leave finishes the run.
	PeerHandoff bool
	// PeerFinisher is true when this peer process is the one guaranteed to
	// leave last, so it finishes the shared run without the refcount.
	PeerFinisher bool

	mu               sync.Mutex
	reportingEnabled bool
	failedReport     int
	unsentResults    int
	lastError        string
}

// NewSession builds a reporting session for a resolved run id.
func NewSession(client *Client, config *Config, testRunID int, ownedByAdapter, peerHandoff bool) *Session {
	return &Session{
		Client:           client,
		Config:           config,
		TestRunID:        testRunID,
		OwnedByAdapter:   ownedByAdapter,
		PeerHandoff:      peerHandoff,
		reportingEnabled: true,
	}
}

// ReportingEnabled is false once the session soft-disabled.
func (s *Session) ReportingEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reportingEnabled
}

// DisableReporting stops further uploads for this process.
func (s *Session) DisableReporting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportingEnabled = false
}

// NoteFailedReport counts a result the server or transport rejected.
func (s *Session) NoteFailedReport(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedReport += n
}

// ClearOneFailedReport undoes one failure count after a later retry landed.
func (s *Session) ClearOneFailedReport() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failedReport > 0 {
		s.failedReport--
	}
}

// FailedReport is the number of results that could not be reported.
func (s *Session) FailedReport() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failedReport
}

// NoteUnsentResults counts results dropped before upload.
func (s *Session) NoteUnsentResults(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsentResults += n
}

// UnsentResults is the number of results never handed to the API.
func (s *Session) UnsentResults() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsentResults
}

// SetLastError records the most recent upload error text.
func (s *Session) SetLastError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = message
}

// LastError returns the most recent upload error text.
func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

// FinishOwnedRun closes the run when this process owns it. Peer sessions are
// not owned — their last release calls Client.FinishRun directly.
func (s *Session) FinishOwnedRun() {
	if !s.OwnedByAdapter {
		return
	}
	if err := s.Client.FinishRun(s.TestRunID); err != nil {
		s.SetLastError(err.Error())
		s.NoteFailedReport(1)
		LogError("Tracera: %s", err)
	}
}

// ResultBuffer batches results and flushes on size / time / end of run.
type ResultBuffer struct {
	session *Session

	mu                       sync.Mutex
	pending                  []*PendingResult
	timer                    *time.Timer
	flushing                 sync.Mutex
	consecutiveBatchFailures int
}

// NewResultBuffer builds the buffer for a session.
func NewResultBuffer(session *Session) *ResultBuffer {
	return &ResultBuffer{session: session}
}

// Enqueue buffers a bound result; unbound results are dropped.
func (b *ResultBuffer) Enqueue(item *PendingResult) {
	if !b.session.ReportingEnabled() || item.Ctx == nil || item.Ctx.TestCaseID() <= 0 {
		return
	}
	b.mu.Lock()
	b.pending = append(b.pending, item)
	full := len(b.pending) >= b.session.Config.FlushMaxItems
	hasTimer := b.timer != nil
	b.mu.Unlock()

	if full {
		go b.Flush()
		return
	}
	if !hasTimer {
		b.scheduleFlush()
	}
}

// PendingCount is how many results are still buffered.
func (b *ResultBuffer) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func (b *ResultBuffer) scheduleFlush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(time.Duration(b.session.Config.FlushMaxMS)*time.Millisecond, func() { b.Flush() })
}

// Flush uploads buffered results now.
// Panics are recovered so a background timer / enqueue goroutine cannot abort
// the test process (parity JobPool recover; transport errors are handled inside).
func (b *ResultBuffer) Flush() {
	defer func() {
		if r := recover(); r != nil {
			LogError("Tracera: flush panic: %v", r)
		}
	}()
	b.flushing.Lock()
	defer b.flushing.Unlock()
	b.flushOnce()
	if b.session.ReportingEnabled() && b.PendingCount() > 0 {
		b.flushOnce()
	}
}

func (b *ResultBuffer) flushOnce() {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if !b.session.ReportingEnabled() || len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	snapshot := b.pending
	b.pending = nil
	b.mu.Unlock()

	var retryLater []*PendingResult
	baseEnv := SnapshotEnvVars(nil)
	idx := 0
	for idx < len(snapshot) && b.session.ReportingEnabled() {
		end := min(idx+b.session.Config.FlushMaxItems, len(snapshot))
		batch := snapshot[idx:end]
		idx = end

		// Attachments must exist on the server before the result references
		// them: resolve any slot a runner enqueued without going through the
		// upload pool. Already-resolved results are a no-op here.
		items := make([]BatchItem, 0, len(batch))
		sent := make([]*PendingResult, 0, len(batch))
		for _, p := range batch {
			payload := b.payloadFor(p)
			if !resolveResultAttachments(b.session, payload) {
				continue
			}
			items = append(items, b.toBatchItem(p, payload, baseEnv))
			sent = append(sent, p)
		}
		if len(items) == 0 {
			continue
		}
		batch = sent
		LogDebug("Tracera: sent %d result(s)", len(items))

		res, err := b.session.Client.PostResultsBatch(b.session.TestRunID, items)
		if err != nil {
			b.session.SetLastError(err.Error())
			LogError("Tracera: %s — requeued", err)
			for _, p := range batch {
				p.transportFailures++
				if p.transportFailures == 1 {
					b.session.NoteFailedReport(1)
				}
			}
			retryLater = append(retryLater, batch...)
			b.consecutiveBatchFailures++
			if b.consecutiveBatchFailures >= ConsecutiveBatchFailLimit {
				b.session.DisableReporting()
				LogError("Tracera: stopped reporting after %d failed batch uploads", ConsecutiveBatchFailLimit)
				break
			}
			continue
		}

		b.consecutiveBatchFailures = 0
		for _, p := range batch {
			if p.transportFailures > 0 {
				b.session.ClearOneFailedReport()
				p.transportFailures = 0
			}
		}
		for _, r := range res.Results {
			if r.OK {
				continue
			}
			b.session.NoteFailedReport(1)
			reason := r.Error
			if reason == "" {
				reason = "item_failed"
			}
			b.session.SetLastError(reason)
			where := fmt.Sprintf("batch index %d", r.Index)
			if r.Index >= 0 && r.Index < len(items) {
				where = fmt.Sprintf("testCaseId=%d", items[r.Index].TestCaseID)
			}
			LogError("Tracera: server rejected result (%s): %s", where, reason)
		}
	}

	// Reporting stopped part-way (consecutive batch failures): the batches
	// never attempted stay buffered so they count as not uploaded instead of
	// vanishing.
	if idx < len(snapshot) {
		retryLater = append(retryLater, snapshot[idx:]...)
	}
	if len(retryLater) > 0 {
		b.mu.Lock()
		b.pending = append(retryLater, b.pending...)
		b.mu.Unlock()
	}
}

// payloadFor freezes the result payload once. Later flush attempts reuse it
// so a requeued result keeps the attachment ids its first attempt resolved.
func (b *ResultBuffer) payloadFor(p *PendingResult) *Payload {
	if p.Payload == nil {
		snapshot := SnapshotPayload(p.Ctx)
		p.Payload = &snapshot
	}
	return p.Payload
}

func (b *ResultBuffer) toBatchItem(p *PendingResult, payload *Payload, baseEnv map[string]string) BatchItem {
	envVars := baseEnv
	if len(p.EnvVarsExtra) > 0 {
		envVars = make(map[string]string, len(baseEnv)+len(p.EnvVarsExtra))
		for k, v := range baseEnv {
			envVars[k] = v
		}
		for k, v := range p.EnvVarsExtra {
			envVars[k] = v
		}
	}
	return BatchItem{
		IsAutomated:     true,
		TestCaseID:      p.Ctx.TestCaseID(),
		TestRunID:       b.session.TestRunID,
		Result:          p.Status,
		AutotestName:    p.Ctx.AutotestName(),
		DurationMinutes: p.DurationMinutes,
		Setup:           orEmptySteps(payload.Setup),
		Test:            orEmptySteps(payload.Test),
		Teardown:        orEmptySteps(payload.Teardown),
		Attachments:     orEmptyAttachments(payload.Attachments),
		ErrorMessage:    nilable(payload.ErrorMessage),
		Comment:         nilable(payload.Comment),
		EnvVars:         envVars,
	}
}

func orEmptySteps(s []StepJSON) []StepJSON {
	if s == nil {
		return []StepJSON{}
	}
	return s
}

func orEmptyAttachments(a []map[string]any) []map[string]any {
	if a == nil {
		return []map[string]any{}
	}
	return a
}

// PrintSummary logs the run link.
func PrintSummary(session *Session) {
	LogLink(session.Client.RunURL(session.TestRunID))
}
