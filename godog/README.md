# tracera-go/godog

Tracera Autotest adapter for Godog.

**Requires Go ≥ 1.25 and Godog ≥ 0.16.**

```bash
go get github.com/tracera-dev/tracera-go/godog
```

Register the suite and wrap the scenario initializer so stock After hooks still
land on Teardown (Godog runs After hooks in registration order):

```go
import traceragodog "github.com/tracera-dev/tracera-go/godog"

suite := godog.TestSuite{
    TestSuiteInitializer: traceragodog.RegisterSuite,
    ScenarioInitializer: traceragodog.Initialize(func(sc *godog.ScenarioContext) {
        sc.Before(setup)
        sc.After(teardown)
        // step definitions …
    }),
}
```

## Bind

Tag a Scenario with `@tracera.id:‹id›`. An optional `@tracera.name:‹name›` tag overrides the reported result name (URL-encode spaces); without it the Scenario name is used:

```gherkin
@tracera.id:101
Scenario: Customer checks out
  Given a seeded cart
  When the customer pays
  Then the order is confirmed
```

Inside a step definition you can also bind imperatively with `tracera.TestCase(101)`.

## Skip

Return `godog.ErrSkip` from a step definition, or filter with `--tags`. The Scenario is reported as Skipped, with no error and no steps. Wrap the sentinel to carry a reason:

```go
func aSeededCart() error {
    return fmt.Errorf("needs a seeded account: %w", godog.ErrSkip)
}
```

The wrapped text becomes the result comment. Pending and undefined steps also report the Scenario as Skipped.

## Steps

Gherkin steps are reported automatically — one Tracera step per `Given` / `When` / `Then`, with the step text as the title. Nest helpers with `tracera.Step`:

```go
func thecustomerPays() error {
    tracera.Step("submit the payment", func() { /* ... */ })
    return nil
}
```

The stock hooks own the lifecycle sections: Before-scenario hooks report into Setup, Gherkin steps into Test, and After-scenario hooks into Teardown. Steps Godog never ran — the ones after a failure or a skip — are left off the card. A Before-scenario hook that fails (returns an error or panics) reports the Scenario Failed with the hook error on the result and no steps.

Prefer `Initialize` so your After hooks run before Tracera closes the result. The lower-level `Register(sc)` closes in an After hook registered at that call — only use it when your reporting After hooks are registered _before_ `Register`.

## Comments, errors, and attachments

`tracera.Comment`, `tracera.Error`, and the attachment helpers attach to the open Gherkin step, or to the result when none is open. Shared placement and `tracera.error` rules: https://tracera.dev/docs/adapters.

Attach a file with `tracera.AttachmentFile` or bytes with `tracera.AttachmentBytes` — they upload when the result is reported. MIME type is optional. Up to ten attachments on a step and ten on the result. Prefer those helpers over `tracera.Attachment` with a pre-uploaded id.

If you start a goroutine and call Tracera APIs there (steps, comments, errors, attachments), save the context on the test goroutine first and run the worker with `tracera.RunWithContext` — otherwise those calls are not reported:

```go
ctx := tracera.GetContext()
go tracera.RunWithContext(ctx, func() { tracera.Step("worker", func() { /* ... */ }) })
```

Running Scenarios in parallel (`godog.Options.Concurrency`) needs no extra setup — each Scenario keeps its own context.

## Parameterized

Scenario Outlines expand natively — the `<column>` placeholders already appear in the Scenario name and the step text, so no Tracera name template is needed. Each Example row reports its own result under the Outline's `@tracera.id:` tag.

Environment variables, run modes and `.env` lookup: [Autotest adapters](https://tracera.dev/docs/adapters).
