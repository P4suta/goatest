# Development

This document describes the infrastructure for working on goatest itself. The
other pages under `docs/` describe the tool; this one describes the tests that
hold it to them. Setup, pull request rules, and source conventions live in
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Test harness

`internal/testkit` holds the fixtures and scripted fakes the test suite is
built from. It is test-only support: no production package may import it. Its
helpers are deterministic, so a fixture, and any golden file derived from one,
does not depend on the machine or the operator that built it.

`testkit` may not be imported by an internal (`package assure`) test of
`internal/assure`, because `events.go` imports that package. Only external
(`package assure_test`) tests can use it. `ScriptedWorkspace` and
`ScriptedSession` satisfy the runner's interfaces structurally, so the rest of
testkit depends on go-mutants and `internal/report` alone.

### Fixture repositories

`NewRepo(t)` starts an empty repository under `t.TempDir()`. Every method
reports failures through the owning test and returns the `*Repo`, so a fixture
reads as one chain:

```go
repository := testkit.NewRepo(t).
	Module("fixture.example/assured").
	File("boundary.go", source).
	Git()

options := assure.Options{Root: repository.Root(), GoBinary: testkit.GoBinary(t)}
```

- `Module(path)` writes `go.mod` for `path` with goatest's own `go` directive.
- `File(path, contents)` writes a slash-separated path verbatim, creating the
  parent directories it needs. Line endings are not rewritten: a fixture that
  asserts on CRLF input must supply CRLF bytes, as the end-to-end tests in
  `internal/assure/run_test.go` do through their local `crlfFixture` helper.
- `BoundaryFixture()` writes the smallest module an assurance run has a verdict
  for: one guarded boundary and the test that pins it. It keeps whatever module
  `Module` already selected.
- `Git()` makes one commit of the whole worktree on branch `main`, with a fixed
  identity and both dates fixed, overriding the operator's global and system
  configuration and their `GIT_AUTHOR_*` and `GIT_COMMITTER_*` environment.
- `Root()` is the absolute repository path; `Path(relative)` resolves a fixture
  path against it.

`GoBinary(t)` resolves the `go` command a test drives a fixture with. It and
`Git()` skip the test when the tool is unavailable rather than failing.

### Scripted command workspaces

`ScriptedWorkspace` answers `assure.CommandWorkspace` calls from a rule table
instead of starting processes, and records every call:

```go
workspace := testkit.NewWorkspace()
workspace.On("go", "test", "-race").Return(gomutants.CommandResult{ExitCode: 1})
workspace.On().Do(func(command gomutants.Command) (gomutants.CommandResult, error) {
	return gomutants.CommandResult{Duration: time.Second}, nil
})

result, err := assure.CollectRace(t.Context(), workspace, model, packages, contract, nil)
commands := workspace.Calls()
```

`On(argvPrefix...)` matches every command whose argv starts with the prefix; an
empty prefix matches everything. The longest matching prefix wins whatever the
registration order, so a specific rule may be added after a general one. A rule
answers with `Return(result)`, `Fail(err)`, or `Do(handler)` for responses that
depend on the command — writing a coverage profile to the path the argv names,
for example. `Return` copies the result when the rule is registered and again
for every response, so neither the test that scripted `Output` nor one that
receives it can change what a later call sees; `Do` answers with whatever its
handler builds.

The fake fails closed: a command no rule covers is still recorded, then
answered with an error wrapping `ErrNoRule` that names the argv, so the code
under test observes the failure it would observe from a broken workspace and
the test reports what it actually attempted.

### Scripted mutation sessions

`ScriptedSession` is the same shape for `assure.MutationSession`. It serves a
fixed catalog and routes executions by mutant identity:

```go
catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}
session := testkit.NewSession(catalog)
session.On(mutant.ID).Return(gomutants.MutantResult{
	ID: mutant.ID, Outcome: gomutants.OutcomeKilled,
})

result, err := assure.EvaluateMutations(t.Context(), session, targets, options)
requests := session.Requests()
```

`On(mutantID, args...)` restricts a rule to the requests whose arguments start
with `args`, so one mutant can answer differently per target; the longest
matching prefix wins. `Catalog()` returns an independent copy on every call,
matching the go-mutants contract the scheduler is written against, `Return`
copies its result the same way `ScriptedWorkspace.Return` does, artifact bytes
included, and an unscripted request fails with `ErrNoRule` exactly as an
unscripted command does.

A fake that has to carry state — counting concurrent executions, or releasing
mutants through a channel — is written by hand in the test that needs it. The
rule table is deliberately stateless.

### Helper subprocesses

A test that must drive a real subprocess scripts one of its own helper tests
instead of building a separate program. `HelperArgv(testName)` is the argument
vector that re-executes the test binary and runs only that test, and
`HelperEnabled(variable)` reports whether the parent activated the helper:

```go
func TestRunResourceProviderHelper(t *testing.T) {
	if !testkit.HelperEnabled("GOATEST_ASSURE_RESOURCE_HELPER") {
		return
	}
	// speak the resource protocol on stdin/stdout
}
```

The helper stays inert during an ordinary run of the package, so it costs
nothing when nobody scripted it. The parent sets the variable with
`t.Setenv`, renders `HelperArgv` into the fixture's provider command, and lets
the runner start it.

### Golden files

`Golden(t, name, got)` compares recorded bytes against `testdata/<name>`,
reporting one failure that names the file. `CompareGolden(path, got, update)`
is the same comparison without the test framework. Without `-update` the
comparison is read-only, and a missing file is a failure rather than a silent
first recording. The `-update` flag is registered once, in testkit, so it means
the same thing in every package; `Update()` reports whether it was set.

`NormalizeReport(input)` replaces the identity fields a run legitimately varies
— run identity, snapshot, git commit and merge base, Go version, and timing —
with the fixed `Normalized*` values, and leaves every other field alone. An
absent field stays absent, the input is not modified, and normalizing an
already normalized report changes nothing. No test in this repository compares
a golden report yet; the helpers exist for the ones that will.

### Progress events

`assure.Run` reports progress as `assure.Event` values. `HasEvent(events,
kind)`, `CountEvent(events, kind)`, and `EventDetails(events, kind)` assert
against a recorded stream — that a warm run reported a cache hit, that a phase
ran once rather than twice, or which targets a phase named:

```go
if got := testkit.EventDetails(events, "baseline-target"); len(got) != 1 {
	t.Fatalf("baseline targets = %v", got)
}
```

These are the only helpers that depend on `internal/assure`, and therefore the
only ones an internal test of that package cannot use.

## TDD workflow

Development is test-driven, in three commits or three steps of one:

1. **Red.** Write the test against the behavior, not the implementation, and
   watch it fail for the stated reason. A test that passes before the change is
   not evidence.
2. **Green.** Make it pass with the smallest change that is honest about the
   contract. Fail-closed behavior is part of the contract, not an error path to
   add later.
3. **Refactor.** Remove the duplication the change introduced, with the suite
   green throughout.

End-to-end tests drive the real toolchain. `internal/assure/run_test.go` is the
reference: it builds a fixture repository, runs `assure.Run` against a real
`go` binary, and asserts on the report and the progress stream rather than on
internal state. Follow that style for anything that crosses a phase boundary,
and keep unit tests on the scripted fakes so the suite stays bounded.

Add `t.Parallel()` wherever the test permits it. Tests that call `t.Setenv`, or
that depend on the working directory, cannot be parallel.

The gates specific to Go and the test harness are `go test ./...`,
`go test -race ./...`, `gofmt -l .`, and `go vet ./...`. They are part of the
gate rather than the whole of it: `mise run check` also checks TOML formatting
with `taplo` and runs `golangci-lint`, `actionlint`, `typos`, and `gitleaks`,
and it runs the suite without the race detector, which `mise run test-race`
adds. [CONTRIBUTING.md](../CONTRIBUTING.md) lists the complete set CI runs.

## Execution tracing

Not yet implemented. There is no tracing facility for a run's internal phases
beyond the progress events described above.

## Failure diagnostics and keep-temp

Not yet implemented. Fixture and run temporary directories are removed by the
test framework when a test finishes, and there is no option to retain them for
inspection after a failure.

## Seam policy

Not yet implemented. The rule that decides which dependencies become injected
interfaces, and which stay concrete, is not written down; `CommandWorkspace`
and `MutationSession` are the existing examples.

## Profiling

Not yet implemented. There is no benchmark or performance contract for the
runner; see [limitations](limitations.md).
