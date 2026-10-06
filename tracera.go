// Package tracera is the customer surface of the Tracera Autotest adapters
// for Go: bind, steps, comments, errors, attachments and context pinning.
//
// Runner wiring (session lifecycle, phases, registry) lives in
// github.com/tracera-dev/tracera-go/plugin and is used by the gotest, ginkgo
// and godog packages — not by test authors.
//
// Customer how-to: https://tracera.dev/docs/adapters
package tracera

import "github.com/tracera-dev/tracera-go/internal/core"

// AttachmentMeta references an uploaded blob on a step or on the result.
type AttachmentMeta = core.AttachmentMeta

// TestCase binds the running test to a Tracera test case id, with an optional
// result name. Runners also expose a declarative bind (gotest helper, Ginkgo
// labels, Godog tags) that works even when the body never runs.
func TestCase(testCaseID int, name ...string) { core.TestCase(testCaseID, name...) }

// Step records fn as a reported step. Steps nest; a panic inside fn fails the
// step and keeps propagating.
func Step(title string, fn func()) { core.Step(title, fn) }

// Setup records fn as a step in the Setup section. The stdlib testing package
// has no per-test hooks, so gotest exposes this section helper; Ginkgo and
// Godog use their own BeforeEach / Before-scenario hooks instead.
func Setup(title string, fn func()) { core.SectionStep(core.SectionSetup, title, fn) }

// Teardown records fn as a step in the Teardown section. See Setup.
func Teardown(title string, fn func()) { core.SectionStep(core.SectionTeardown, title, fn) }

// Comment adds text to the open step, or to the result when no step is open.
func Comment(text string) { core.Comment(text) }

// Error adds error text to the open step, or to the result when no step is
// open. A soft Error does not by itself fail the test.
func Error(text string) { core.Error(text) }

// Attachment attaches a blob that is already uploaded (you hold its Tracera
// attachment id) to the open step or result. To attach a file or bytes, use
// AttachmentFile or AttachmentBytes.
func Attachment(meta AttachmentMeta) { core.Attachment(meta) }

// AttachmentFile attaches a file from disk to the open step, or to the result
// when no step is open. The file is read when the result is reported, so it
// must still exist then. The MIME type is optional — it is guessed from the
// file extension when omitted.
func AttachmentFile(path string, mimeType ...string) { core.AttachmentFile(path, mimeType...) }

// AttachmentBytes attaches in-memory bytes to the open step, or to the result
// when no step is open. The bytes are copied so later mutation of the caller's
// slice cannot change what is uploaded. The MIME type is optional — it is
// guessed from the file name when omitted.
func AttachmentBytes(fileName string, data []byte, mimeType ...string) {
	core.AttachmentBytes(fileName, data, mimeType...)
}

// GetContext captures the active pin so a worker goroutine can report into
// the same test. Returns nil outside a reported test.
func GetContext() *TestContext { return wrapPin(core.GetContext()) }

// RunWithContext pins ctx while fn runs on the current goroutine. Goroutines
// do not inherit the pin — capture GetContext first, then wrap the worker
// body with this.
func RunWithContext(ctx *TestContext, fn func()) {
	core.RunWithContext(unwrapPin(ctx), fn)
}
