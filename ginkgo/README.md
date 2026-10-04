# tracera-go/ginkgo

Tracera Autotest adapter for Ginkgo v2.

**Requires Go ≥ 1.25 and Ginkgo v2.**

```bash
go get github.com/tracera-dev/tracera-go/ginkgo
```

Call `Register()` once in the suite file:

```go
import traceraginkgo "github.com/tracera-dev/tracera-go/ginkgo"

var _ = traceraginkgo.Register()
```

## Bind

Bind a spec or a container with a `tracera.id:‹id›` Label; containers pass it down to their specs. An optional `tracera.name:‹name›` Label overrides the reported result name (URL-encode spaces):

```go
var _ = Describe("checkout", Label("tracera.id:101"), func() {
    It("pays", func() { /* ... */ })
})
```

Inside a spec you can also bind imperatively with `tracera.TestCase(101)`.

## Skip

Use the stock Ginkgo skip APIs — `Skip("reason")`, `PIt`, `XDescribe` or `Pending`. The reason is reported as a comment and the result is Skipped, with no error and no steps.

## Steps

Use `tracera.Step("…", func() { … })`. Go has no method-step decorator. Steps inside `BeforeEach` / `AfterEach` go to **Setup** / **Teardown**; steps inside the spec go to **Test**. Suite-level hooks (`BeforeSuite` / `AfterSuite`) are not reported.

```go
It("pays", func() {
    tracera.Step("open the cart", func() {
        tracera.Step("add an item", func() { /* ... */ })
    })
})
```

## Comments, errors, and attachments

`tracera.Comment`, `tracera.Error`, and the attachment helpers attach to the open step, or to the result when no step is open. Shared placement and `tracera.error` rules: https://tracera.dev/docs/adapters.

Attach a file with `tracera.AttachmentFile` or bytes with `tracera.AttachmentBytes` — they upload when the result is reported. MIME type is optional. Up to ten attachments on a step and ten on the result. Prefer those helpers over `tracera.Attachment` with a pre-uploaded id.

If you start a goroutine and call Tracera APIs there (steps, comments, errors, attachments), save the context on the test goroutine first and run the worker with `tracera.RunWithContext` — otherwise those calls are not reported:

```go
ctx := tracera.GetContext()
go tracera.RunWithContext(ctx, func() { tracera.Step("worker", func() { /* ... */ }) })
```

Parallel suites (`ginkgo -p`, `--procs=N`) need no extra setup: the processes agree on a single test run between themselves, each uploads its own specs, and the run is closed once the last process finished. Flaky reruns (`--flake-attempts`) report the final attempt and add a `GINKGO_RETRY` entry to the Env tab.

## Parameterized

Use Ginkgo `DescribeTable` / `Entry`. Put a fixed `tracera.id:` on a Label (table or entry). If the name is omitted, `autotestName` takes the value of the Ginkgo test name — give each `Entry` its own title. Or pass a name to `tracera.TestCase`:

```go
DescribeTable("checkout", Label("tracera.id:101"),
    func(qty int) { /* ... */ },
    Entry("one item", 1),
    Entry("two items", 2),
)
```

Environment variables, run modes and `.env` lookup: [Autotest adapters](https://tracera.dev/docs/adapters).
