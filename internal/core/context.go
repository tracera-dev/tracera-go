package core

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// StepSection is the lifecycle bucket a step lands in.
type StepSection string

// Lifecycle sections of a reported result.
const (
	SectionSetup    StepSection = "setup"
	SectionTest     StepSection = "test"
	SectionTeardown StepSection = "teardown"
)

const maxAttachments = 10

// AttachmentMeta references an uploaded blob on a step or on the result.
type AttachmentMeta struct {
	AttachmentID string
	FileName     string
	MimeType     string
}

func (m AttachmentMeta) toWire() map[string]any {
	out := map[string]any{"attachmentId": m.AttachmentID}
	if m.FileName != "" {
		out["fileName"] = m.FileName
	}
	if m.MimeType != "" {
		out["mimeType"] = m.MimeType
	}
	return out
}

type stepNode struct {
	id           int
	parent       *stepNode
	parentID     *int
	section      StepSection
	result       string
	action       any
	expectation  any
	attachments  []map[string]any
	errorMessage string
	comment      string
	children     []*stepNode
}

// TestContext is the active test pin: bind, section, step tree and extras.
type TestContext struct {
	mu                sync.Mutex
	testCaseID        int
	autotestName      string
	section           StepSection
	resultError       string
	resultComment     string
	stepStack         []*stepNode
	roots             map[StepSection][]*stepNode
	resultAttachments []map[string]any
	nextStepID        int
}

// CreateTestContext builds an unbound-or-bound context for one test.
func CreateTestContext(testCaseID int, autotestName string) *TestContext {
	return &TestContext{
		testCaseID:   testCaseID,
		autotestName: truncate(autotestName, 128),
		section:      SectionTest,
		roots:        map[StepSection][]*stepNode{SectionSetup: nil, SectionTest: nil, SectionTeardown: nil},
		nextStepID:   1,
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// TestCaseID returns the bound Tracera test case id (0 when unbound).
func (c *TestContext) TestCaseID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.testCaseID
}

// SetTestCaseID binds (or rebinds) the context to a Tracera test case.
func (c *TestContext) SetTestCaseID(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.testCaseID = id
}

// AutotestName returns the reported result name.
func (c *TestContext) AutotestName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.autotestName
}

// SetAutotestName overrides the reported result name (capped at 128 runes).
func (c *TestContext) SetAutotestName(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.autotestName = truncate(name, 128)
}

// Section reports which lifecycle bucket new steps land in.
func (c *TestContext) Section() StepSection {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.section
}

// SetSection moves later steps into setup / test / teardown.
func (c *TestContext) SetSection(section StepSection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.section = section
}

// ResultError returns the result-level error text (fail XOR step errors).
func (c *TestContext) ResultError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resultError
}

var ctxStore sync.Map // goroutine id -> *TestContext

var goroutinePrefix = []byte("goroutine ")

// goID reads the current goroutine id. Go has no goroutine-local storage, so
// the active test pin is keyed by this id (the stdlib `testing` runner keeps
// one goroutine per test, including across t.Parallel).
func goID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]
	line = bytes.TrimPrefix(line, goroutinePrefix)
	if i := bytes.IndexByte(line, ' '); i >= 0 {
		line = line[:i]
	}
	id, err := strconv.ParseInt(string(line), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// processCtx is the fallback pin for runners that execute one scenario at a
// time per process but spread its hooks and body across goroutines (Ginkgo
// nodes, Godog steps). Runners that overlap tests inside one process — the
// stdlib `testing` package with t.Parallel — must not use it.
var processCtx atomic.Pointer[TestContext]

// GetContext returns the pin for the current goroutine, falling back to the
// process-wide pin, or nil.
func GetContext() *TestContext {
	if v, ok := ctxStore.Load(goID()); ok {
		if ctx, _ := v.(*TestContext); ctx != nil {
			return ctx
		}
	}
	return processCtx.Load()
}

// InstallContext pins ctx on the current goroutine until ClearContext.
func InstallContext(ctx *TestContext) {
	if ctx == nil {
		ClearContext()
		return
	}
	ctxStore.Store(goID(), ctx)
}

// ClearContext drops the pin for the current goroutine.
func ClearContext() {
	ctxStore.Delete(goID())
}

// InstallProcessContext pins ctx for every goroutine in this process that has
// no pin of its own. Only runners that execute one test at a time per process
// may use it.
func InstallProcessContext(ctx *TestContext) { processCtx.Store(ctx) }

// ClearProcessContext drops the process-wide pin.
func ClearProcessContext() { processCtx.Store(nil) }

// RunWithContext pins ctx for the duration of fn on the current goroutine.
// Goroutines do not inherit the pin — capture GetContext on the test goroutine
// and wrap the worker body with this.
func RunWithContext(ctx *TestContext, fn func()) {
	previous := GetContext()
	InstallContext(ctx)
	defer func() {
		if previous != nil {
			InstallContext(previous)
		} else {
			ClearContext()
		}
	}()
	fn()
}

// SetPhase moves the active context into a lifecycle section (plugin tier).
func SetPhase(section StepSection) {
	if ctx := GetContext(); ctx != nil {
		ctx.SetSection(section)
	}
}

func emptyPMDoc(text string) map[string]any {
	trimmed := text
	if trimmed == "" {
		return map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph"}}}
	}
	return map[string]any{
		"type": "doc",
		"content": []any{map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": trimmed}},
		}},
	}
}

// StepHandle is an open step. Close it exactly once.
type StepHandle struct {
	ctx  *TestContext
	node *stepNode
}

// BeginStep opens a step under the active context. A nil pin yields a no-op
// handle, so steps on a foreign goroutine are dropped rather than panicking.
func BeginStep(title string) *StepHandle {
	ctx := GetContext()
	if ctx == nil {
		return &StepHandle{}
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	node := &stepNode{
		id:          ctx.nextStepID,
		section:     ctx.section,
		result:      "NotSet",
		action:      emptyPMDoc(title),
		expectation: emptyPMDoc(""),
	}
	ctx.nextStepID++
	if n := len(ctx.stepStack); n > 0 {
		parent := ctx.stepStack[n-1]
		parentID := parent.id
		node.parent = parent
		node.parentID = &parentID
		parent.children = append(parent.children, node)
	} else {
		ctx.roots[ctx.section] = append(ctx.roots[ctx.section], node)
	}
	ctx.stepStack = append(ctx.stepStack, node)
	return &StepHandle{ctx: ctx, node: node}
}

// Fail marks the open step Failed with the given error / panic value.
func (h *StepHandle) Fail(err any) {
	if h == nil || h.node == nil {
		return
	}
	h.ctx.mu.Lock()
	defer h.ctx.mu.Unlock()
	h.node.result = "Failed"
	for _, child := range h.node.children {
		if child.errorMessage != "" {
			return
		}
	}
	h.node.errorMessage = AppendText(h.node.errorMessage, FormatFailureText(err))
}

// Discard drops an open step from the tree. Runners use it for steps the
// framework never executed (a Gherkin step after a skip / pending step), which
// must not show up in Steps at all. Pending blob bodies registered on the
// dropped step (and its children) are released so they do not linger until
// ClearPendingBlobs / process exit.
func (h *StepHandle) Discard() {
	if h == nil || h.node == nil {
		return
	}
	ctx, node := h.ctx, h.node
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	discardNodeBlobs([]*stepNode{node})
	if n := len(ctx.stepStack); n > 0 && ctx.stepStack[n-1] == node {
		ctx.stepStack = ctx.stepStack[:n-1]
	}
	if node.parent != nil {
		node.parent.children = withoutNode(node.parent.children, node)
	} else {
		ctx.roots[node.section] = withoutNode(ctx.roots[node.section], node)
	}
	h.ctx = nil
	h.node = nil
}

func withoutNode(nodes []*stepNode, drop *stepNode) []*stepNode {
	out := nodes[:0]
	for _, n := range nodes {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// Close finishes the step, defaulting an untouched step to Passed.
func (h *StepHandle) Close() {
	if h == nil || h.node == nil {
		return
	}
	ctx, node := h.ctx, h.node
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if node.result == "NotSet" {
		node.result = "Passed"
	}
	if n := len(ctx.stepStack); n > 0 && ctx.stepStack[n-1] == node {
		ctx.stepStack = ctx.stepStack[:n-1]
	}
	h.ctx = nil
	h.node = nil
}

// Step records fn as a step. A panic inside fn fails the step and propagates.
// So does a runtime.Goexit that unwinds through it — t.Fatal / t.FailNow /
// require.* end the test goroutine without a panic, and the step they ran in
// must not close as Passed. (t.SkipNow also exits this way; a Skipped result
// drops its steps, so marking the step here is harmless.)
func Step(title string, fn func()) {
	h := BeginStep(title)
	returned := false
	defer func() {
		if r := recover(); r != nil {
			h.Fail(Failure{Message: FormatFailureText(r), Stack: string(capturedStack())})
			h.Close()
			panic(r)
		}
		if !returned {
			h.Fail(Failure{
				Message: "test goroutine exited inside this step (t.Fatal / t.FailNow / require failure) — " +
					"the assertion text stays in the go test output",
				Stack: string(capturedStack()),
			})
		}
		h.Close()
	}()
	fn()
	returned = true
}

func capturedStack() []byte {
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

// SectionStep runs fn inside a section (gotest Setup / Teardown helpers):
// the section is restored afterwards so the test body keeps reporting to Test.
func SectionStep(section StepSection, title string, fn func()) {
	ctx := GetContext()
	if ctx == nil {
		fn()
		return
	}
	previous := ctx.Section()
	ctx.SetSection(section)
	defer ctx.SetSection(previous)
	Step(title, fn)
}

func (c *TestContext) currentStep() *stepNode {
	if n := len(c.stepStack); n > 0 {
		return c.stepStack[n-1]
	}
	return nil
}

// Comment appends text to the open step, or to the result when none is open.
func Comment(text string) {
	ctx := GetContext()
	if ctx == nil {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if cur := ctx.currentStep(); cur != nil {
		cur.comment = AppendText(cur.comment, text)
		return
	}
	ctx.resultComment = AppendText(ctx.resultComment, text)
}

// Error appends soft error text to the open step, or to the result when none
// is open. It does not by itself flip the result to Failed.
func Error(text string) {
	ctx := GetContext()
	if ctx == nil {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if cur := ctx.currentStep(); cur != nil {
		cur.errorMessage = AppendText(cur.errorMessage, text)
		return
	}
	ctx.resultError = AppendText(ctx.resultError, text)
}

// Attachment attaches an uploaded blob to the open step or to the result.
func Attachment(meta AttachmentMeta) { tryAttachment(meta) }

// tryAttachment claims a slot on the open step, or on the result when no step
// is open. It reports false when the slot was dropped: no pin, no id, or the
// owner already holds maxAttachments.
func tryAttachment(meta AttachmentMeta) bool {
	ctx := GetContext()
	if ctx == nil || meta.AttachmentID == "" {
		return false
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if cur := ctx.currentStep(); cur != nil {
		if len(cur.attachments) >= maxAttachments {
			return false
		}
		cur.attachments = append(cur.attachments, meta.toWire())
		return true
	}
	if len(ctx.resultAttachments) >= maxAttachments {
		return false
	}
	ctx.resultAttachments = append(ctx.resultAttachments, meta.toWire())
	return true
}

// AttachmentFile attaches a file from disk to the open step or to the result.
// The blob stays on disk and streams when the result is reported.
func AttachmentFile(path string, mimeType ...string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return
	}
	fileName := filepath.Base(trimmed)
	attachPending(fileName, resolveAttachmentMime(fileName, mimeType), nil, trimmed)
}

// AttachmentBytes attaches in-memory bytes to the open step or to the result.
// The bytes are copied so later mutation of the caller's slice cannot change
// what is uploaded.
func AttachmentBytes(fileName string, data []byte, mimeType ...string) {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "file"
	}
	var copyData []byte
	if len(data) > 0 {
		copyData = append([]byte(nil), data...)
	}
	attachPending(name, resolveAttachmentMime(name, mimeType), copyData, "")
}

func attachPending(fileName, mimeType string, data []byte, path string) {
	id := NewPendingAttachmentID()
	// Register the body only once the slot is accepted, so a dropped
	// attachment cannot leave an orphan blob behind.
	if !tryAttachment(AttachmentMeta{AttachmentID: id, FileName: fileName, MimeType: mimeType}) {
		return
	}
	RegisterPendingBlob(id, NewPendingBlob(fileName, mimeType, data, path))
}

// StepJSON is one flattened step on the wire.
type StepJSON struct {
	ID           int              `json:"id"`
	ParentID     *int             `json:"parentId"`
	Result       string           `json:"result"`
	Action       any              `json:"action"`
	Expectation  any              `json:"expectation"`
	Attachments  []map[string]any `json:"attachments"`
	ErrorMessage *string          `json:"errorMessage"`
	Comment      *string          `json:"comment"`
}

// Payload is the customer-visible snapshot of one test result.
type Payload struct {
	Setup        []StepJSON
	Test         []StepJSON
	Teardown     []StepJSON
	Attachments  []map[string]any
	ErrorMessage string
	Comment      string
}

func nilable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func flattenSteps(nodes []*stepNode) []StepJSON {
	out := make([]StepJSON, 0, len(nodes))
	var walk func(n *stepNode)
	walk = func(n *stepNode) {
		attachments := n.attachments
		if attachments == nil {
			attachments = []map[string]any{}
		}
		out = append(out, StepJSON{
			ID:           n.id,
			ParentID:     n.parentID,
			Result:       n.result,
			Action:       n.action,
			Expectation:  n.expectation,
			Attachments:  attachments,
			ErrorMessage: nilable(n.errorMessage),
			Comment:      nilable(n.comment),
		})
		for _, child := range n.children {
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}

// SnapshotPayload freezes the step tree and extras for upload.
func SnapshotPayload(ctx *TestContext) Payload {
	if ctx == nil {
		return Payload{Setup: []StepJSON{}, Test: []StepJSON{}, Teardown: []StepJSON{}, Attachments: []map[string]any{}}
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	attachments := ctx.resultAttachments
	if attachments == nil {
		attachments = []map[string]any{}
	}
	return Payload{
		Setup:        flattenSteps(ctx.roots[SectionSetup]),
		Test:         flattenSteps(ctx.roots[SectionTest]),
		Teardown:     flattenSteps(ctx.roots[SectionTeardown]),
		Attachments:  attachments,
		ErrorMessage: ctx.resultError,
		Comment:      ctx.resultComment,
	}
}

// PayloadHasStepError reports whether any step already carries the failure, so
// runners keep fail text XOR between step and result.
func PayloadHasStepError(p Payload) bool {
	for _, group := range [][]StepJSON{p.Setup, p.Test, p.Teardown} {
		for _, s := range group {
			if s.ErrorMessage != nil && *s.ErrorMessage != "" {
				return true
			}
		}
	}
	return false
}

// PayloadHasFailedStep reports whether any step finished Failed.
func PayloadHasFailedStep(p Payload) bool {
	for _, group := range [][]StepJSON{p.Setup, p.Test, p.Teardown} {
		for _, s := range group {
			if s.Result == "Failed" {
				return true
			}
		}
	}
	return false
}

// RecordFailure puts fail text on the open step, else on the result.
func RecordFailure(ctx *TestContext, err any) {
	if ctx == nil {
		return
	}
	msg := FormatFailureText(err)
	if msg == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if cur := ctx.currentStep(); cur != nil {
		cur.result = "Failed"
		cur.errorMessage = AppendText(cur.errorMessage, msg)
		return
	}
	ctx.resultError = AppendText(ctx.resultError, msg)
}

// RecordSkip stores the skip reason as a result comment (never an error).
func RecordSkip(ctx *TestContext, reason string) {
	if ctx == nil {
		return
	}
	text := NormalizeSkipReason(reason)
	if text == "" {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.resultComment = AppendText(ctx.resultComment, text)
}

// MarkSkipped shapes a Skipped result: the reason becomes the result comment,
// the recorded steps are dropped and result-level fail text is cleared. A
// Skipped result reports no steps and no errorMessage on every language.
func MarkSkipped(ctx *TestContext, reason string) {
	if ctx == nil {
		return
	}
	RecordSkip(ctx, reason)
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.resultError = ""
	ctx.clearStepTreesLocked()
}

// ResetForRetry drops everything a previous attempt recorded — steps,
// comments, errors and attachments — so a retried test reports its final
// attempt only. The bind (test case id and result name) is kept.
func ResetForRetry(ctx *TestContext) {
	if ctx == nil {
		return
	}
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.resultError = ""
	ctx.resultComment = ""
	discardPendingList(ctx.resultAttachments)
	ctx.resultAttachments = nil
	ctx.nextStepID = 1
	ctx.clearStepTreesLocked()
}

// clearStepTreesLocked drops every recorded step and the blob bodies queued
// behind their attachments, so a skipped or retried attempt does not keep
// its bytes registered until the end of the run.
func (c *TestContext) clearStepTreesLocked() {
	for _, roots := range c.roots {
		discardNodeBlobs(roots)
	}
	c.stepStack = nil
	c.roots = map[StepSection][]*stepNode{SectionSetup: nil, SectionTest: nil, SectionTeardown: nil}
}

// TestCase binds the active context to a Tracera test case id, with an
// optional result name.
func TestCase(testCaseID int, name ...string) {
	ctx := GetContext()
	if ctx == nil {
		return
	}
	ctx.SetTestCaseID(testCaseID)
	if len(name) > 0 && name[0] != "" {
		ctx.SetAutotestName(name[0])
	}
}
