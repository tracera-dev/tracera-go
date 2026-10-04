# tracera-go/gotest

Tracera Autotest adapter for the Go standard-library `testing` package.

**Requires Go ≥ 1.25.**

```bash
go get github.com/tracera-dev/tracera-go/gotest
```

Wire reporting once from `TestMain` — without it buffered results can be lost and the run is never closed (the adapter warns once):

```go
func TestMain(m *testing.M) { os.Exit(gotest.Run(m)) }
```

## Bind

Bind at the top of a test. The Go test name is reported unless you pass a name:

```go
func TestCheckout(t *testing.T) {
    gotest.TestCase(t, 101)                   // reports as "TestCheckout"
    // gotest.TestCase(t, 101, "checkout flow")
}
```

The result is uploaded from a `t.Cleanup` that runs after every other cleanup, so late assertions and deferred work still land on the card. A test that never binds reports nothing.

In a parallel test, call `t.Parallel()` **before** `gotest.TestCase`: the reported duration starts counting at the bind, and a later `t.Parallel()` blocks until the parent test returns.

```go
func TestCheckout(t *testing.T) {
    t.Parallel()
    gotest.TestCase(t, 101)
}
```

## Skip

Use `gotest.Skip` / `gotest.Skipf` so the reason is added to the result as `comment` (stock `t.Skip` alone does not):

```go
gotest.Skip(t, "needs a seeded account")
```

Skipped results carry the reason as a comment, with no error and no steps.

## Steps

Use `tracera.Step("…", func() { … })`. Go has no method-step decorator. Steps nest freely:

```go
tracera.Step("open the cart", func() {
    tracera.Step("add an item", func() { /* ... */ })
})
```

The `testing` package has no per-test hooks, so Setup and Teardown sections come from the section helpers:

```go
tracera.Setup("seed the account", func() { /* ... */ })
tracera.Step("check out", func() { /* ... */ })
tracera.Teardown("drop the account", func() { /* ... */ })
```

## Comments, errors, and attachments

`tracera.Comment`, `tracera.Error`, and the attachment helpers attach to the open step, or to the result when no step is open. Shared placement and `tracera.error` rules: https://tracera.dev/docs/adapters.

Attach a file from disk with `tracera.AttachmentFile` (uploaded when the result is reported — the file must still exist then) or in-memory bytes with `tracera.AttachmentBytes` (the bytes are copied). MIME type is optional. Up to ten attachments on a step and ten on the result.

```go
tracera.Step("charge the card", func() {
    tracera.Comment("sandbox gateway")
    tracera.Error("retried once")
    tracera.AttachmentFile("testdata/receipt.pdf")
    tracera.AttachmentBytes("response.json", body, "application/json")
})
```

`tracera.Attachment` accepts an already-uploaded blob id — use it only when you hold a Tracera attachment id.

A panic inside a step records the message and the captured stack on that step; a `t.Fatalf` (or `require.*` failure) inside a step marks that step Failed too. The `testing` package keeps `t.Errorf` / `t.Fatalf` text to itself, so a test that fails that way reports the test name plus the source locations Tracera can still resolve — pass the message yourself when you want it on the card:

```go
if err != nil {
    tracera.Error(err.Error())
    t.Fatalf("checkout failed: %v", err)
}
```

If you start a goroutine and call Tracera APIs there (steps, comments, errors, attachments), save the context on the test goroutine first and run the worker with `tracera.RunWithContext` — otherwise those calls are not reported:

```go
ctx := tracera.GetContext()
go tracera.RunWithContext(ctx, func() { tracera.Step("worker", func() { /* ... */ }) })
```

`t.Parallel()` is supported — each test keeps its own context.

## Parameterized

Table-driven tests report one result per bound subtest:

```go
for _, row := range rows {
    t.Run(row.name, func(t *testing.T) {
        gotest.TestCase(t, row.caseID, row.name)
        tracera.Step("check out", func() { /* ... */ })
    })
}
```

Environment variables, run modes and `.env` lookup: [Autotest adapters](https://tracera.dev/docs/adapters).
