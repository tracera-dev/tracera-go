# tracera-go

Tracera Autotest adapter core for Go (HTTP, flush, log, dotenv, Env, context).

**Requires Go ≥ 1.25.**

```bash
go get github.com/tracera-dev/tracera-go
```

```go
import tracera "github.com/tracera-dev/tracera-go"
```

Pick a runner package unless you are writing a custom integration: `…/gotest`, `…/ginkgo`, `…/godog`.

## Bind

Declarative bind lives on the runner (gotest helper, Ginkgo `tracera.id:` Labels, Godog `@tracera.id:` tags). Inside a running test you can also rebind imperatively:

```go
tracera.TestCase(101)                   // id only — the runner supplies the name
tracera.TestCase(101, "checkout flow")  // id + reported result name
```

## Skip

Use the runner's skip API so the reason becomes the result `comment`. Stock skip helpers that hide the message from reporters need the runner wrapper (documented on that runner).

## Steps

Go has no method decorators, so steps are call-form only. They nest, and a panic inside a step marks it Failed with the captured stack before propagating, and so does a `t.Fatal` / `require.*` failure that ends the test inside it:

```go
tracera.Step("open the cart", func() {
    tracera.Step("add an item", func() { /* ... */ })
})
```

`tracera.Setup` and `tracera.Teardown` put a step in the Setup or Teardown section when the runner has no per-test hooks (stdlib `testing`). Ginkgo and Godog map their stock Before/After hooks instead.

## Comments, errors, and attachments

`tracera.Comment`, `tracera.Error` and the attachment helpers land on the open step, or on the result when no step is open. `tracera.Error` only adds error text on the step or result — it does not fail the test; the runner marks **Failed**.

Attach a file from disk with `tracera.AttachmentFile` (it is uploaded when the result is reported, so the file must still exist then) or in-memory bytes with `tracera.AttachmentBytes` (the bytes are copied). The MIME type is optional. Up to ten attachments land on a step, and ten more on the result.

```go
tracera.Step("charge the card", func() {
    tracera.Comment("sandbox gateway")
    tracera.Error("retried once")
    tracera.AttachmentFile("testdata/receipt.pdf")
    tracera.AttachmentBytes("response.json", body, "application/json")
})
```

`tracera.Attachment` takes a blob that is already uploaded — reach for it only when you hold a Tracera attachment id.

Goroutines do not inherit the active test. Capture the context and pin it in the worker:

```go
ctx := tracera.GetContext()
go tracera.RunWithContext(ctx, func() {
    tracera.Step("background check", func() { /* ... */ })
})
```

## Parameterized

Table-driven and Outline-style suites report one result per bound row. Bind with the id for that row (runner helper or mid-body `tracera.TestCase`); keep the reported name literal or use the native table/Outline title — there is no Tracera `{param}` template on Go tags.

## Several test processes

Usual case: one process, one test run — nothing extra to set. Ginkgo parallel (`ginkgo -p`) already puts its processes into one run.

Only when **you** start several OS processes and want them in the **same** run: give each a different `TRACERA_PEER_WORKER` for that launch. The first process opens the run; the last closes it. You do not create the run by hand.

Environment variables and `.env` lookup: [Autotest adapters](https://tracera.dev/docs/adapters).
