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

`goatest verify --trace[=DIR]` and `goatest replay ID --trace[=DIR]` record
what a run did while it did it. Each run records into a directory of its own,
`<UTC timestamp>-<pid>/`, under the trace root the flag names; without a
directory that root is `.goatest/trace/`, which the source snapshot never
reads, and naming one collects its recordings there instead; `GOATEST_TRACE=1` asks for the same location and `GOATEST_TRACE=DIR`
for a named one, so a job that cannot change a command line can still ask.
The environment variable is read in `cmd/goatest` alone, where it becomes the
flag the command layer parses: no layer below the command line reads the
environment.

A run that asked for no trace still records, into a ring of its last 4096 events
in memory: no file, no directory, and a bounded price a run of any length pays
once. Nothing reads that ring unless the run fails, and then it becomes the
`trace.jsonl` of the diagnostics bundle below. That is what lets a failure
nobody expected be read as a recording, when nobody thought to pass `--trace`
for it.

A trace directory holds `trace.jsonl`, one JSON object per line in sequence
order, and `output/<seq>.txt`, the captured output of the commands that
produced any. The stream, its nine event types, and the fields of each are
specified in [trace v1](trace-v1.md); the rules behind them are recorded in
[ADR 0002](adr/0002-trace-is-not-evidence.md). In short: a trace is never
evidence, nothing about one changes what a run decides — a sink that fails
costs a `trace-unavailable` note and never the run — and it is honest about
what it dropped.

### Recording from a new call site

The recorder is a `*trace.Recorder` reached through the options of the package
that owns the work — `assure.Options`, `MutationOptions`,
`RepositoryValidatorOptions`, `mutationbridge.Options` — and constructed once,
in `internal/app`, from the request. There is no package-level recorder and no
global seam: a test that wants a recording builds the options with one.

Record unconditionally. Every method of a `*trace.Recorder` is safe on a nil
receiver, so `options.Trace.Route(...)` costs a nil check where a caller passed
no recorder, and no call site branches on whether tracing is on. That is what
keeps the traced and the untraced path one path.

The nil recorder and the recording every run keeps are two different mechanisms,
and only the first of them is free. The nil recorder belongs to `internal/trace`:
it is the disabled trace a caller that passes none is left with, which is how a
unit test builds options without a recording. A run of the tool is never in that
position. `internal/app` opens a recording for every run and hands it down, so a
run that asked for no `--trace` is still handed a live recorder and still
records — into the bounded ring above rather than into nothing.

Two constraints hold below the command layer. `internal/assure` and
`internal/trace` read no environment variables — `GOATEST_TRACE` becomes a flag
in `cmd/goatest` and nothing under it learns the environment exists — and no
trace option may enter `modeIdentity`, the assurance inputs, or the evidence
digest, because a run must decide the same thing traced and untraced.

### Testing a recording

- `trace.NewMemorySink(capacity)` keeps events in a ring buffer and returns
  them from `Events()`, which is how a unit test asserts on a stream without
  writing a directory. A capacity of zero or less is unbounded; a full ring
  drops its oldest event and counts it, which is the loss a `run-end` reports.
  A bounded ring fills `capacity-1` slots while a run is under way and keeps
  the last for the `run-end`, so the event carrying the accounting never
  displaces one the accounting had already counted. Events are cloned on the
  way in and on the way out, so a test may amend the record it emitted or the
  snapshot it read without touching the ring.
- `trace.NewDirSink(root, run, hooks)` opens the recording of one run in its
  own directory under a trace root, and reports it through `Directory()`. The
  run directory is created exclusively: a name another recording owns is an
  error, never an append.
- `trace.Filesystem` is the filesystem a `DirSink` writes through, passed to
  `NewDirSink` as an ordinary argument. Fill in only the operation a test wants
  to drive — `MkdirAll`, `Mkdir`, `OpenAppend`, or `WriteFile` — and the rest
  comes from the `os` package.
- `trace.JSONSchema()` returns the embedded schema.
  `internal/trace/schema_test.go` validates recorded events against it with the
  same compiler the report schema uses, and `internal/app/trace_e2e_test.go`
  validates every line of a real `verify --trace`, decoding with
  `DisallowUnknownFields` so a field outside the contract fails too.

A new event or a new call site is proven end to end there: the test drives
`cli.Run` over a real `go` on a `testkit` fixture and asserts on the recording
against the report the same run produced — that every phase is closed before
the next one opens, that a mutant's `route` precedes every `mutant-exec` that
names it, and that the accounting in `run-end` matches the lines in the file.

## Failure diagnostics and keep-temp

A run that ends in an error leaves a bundle of what it knew behind it, and
`--keep-temp` leaves the directories it would otherwise have removed. Both are
diagnostic exhaust in the sense [ADR 0002](adr/0002-trace-is-not-evidence.md)
fixes for a trace: they are best-effort, they take no part in a verdict or in
the identity a cached result is keyed on, and what they could not do they report
rather than hide.

### The failure diagnostics bundle

`internal/app` writes one when the assurance run behind `verify` or `replay`
returns an error — the branch of `runAndWrite` that turns that error into an
`ERROR` report. A run that ended in `context.Canceled` returns before it: an
interrupt is not a failure to diagnose. The bundle is written from the finalized
report, so nothing in it can reach the report, the verdict, or the exit code.

It lands in `<repository>/.goatest/diagnostics/<run>/`, one directory per failed
run. `<run>` is the run identity of the report; a run that stopped before it had
one — the failure a bundle is most needed for — is named `<UTC timestamp>-<pid>`
instead, the name a recording of the same run takes, from the same injected
clock and process id.

| File | What it holds |
| --- | --- |
| `trace.jsonl` | the events of the recording the run kept in memory, as the JSON Lines stream and under the `goatest-trace-v1` schema a trace directory holds |
| `error.txt` | the run identity and verdict, the error as `%+v`, then one line per error behind it, named by its type and indented by how far behind the first it is, following every branch of a joined error |
| `environment.txt` | the toolchain that decided the result, the `go` binary and temporary directory it used, then the environment variable *names*, sorted and deduplicated |
| `preserved-paths.txt` | a `kind`/`path` table of what the run left on disk: the trace directory when it recorded into one, and every `artifact` event of the recording |

A file with nothing to say is not written, because an empty file in a bundle
reads as a fact about the run rather than as the absence of one. A run that
traced to a directory kept its stream there in full, so its bundle carries no
`trace.jsonl` and its `preserved-paths.txt` names that directory instead of
repeating a part of it. `preserved-paths.txt` itself is always written, saying
`# this run left nothing behind` when there was nothing, because a reader must
be able to tell an empty answer from a missing one.

`environment.txt` names the environment and never quotes it, exactly as an
`exec` event does. That is what makes a bundle safe to attach to a bug report
from a machine holding real credentials.

The bundle reports itself on the progress stream, which is stderr for the
binary: a `diagnostics` note saying where it was written, and a single
`diagnostics-unavailable` note carrying everything it could not write. Neither
changes the exit code, and a bundle that failed entirely leaves a failed run
reported exactly as it would have been reported without one.

`Service.DiagnosticsFilesystem` is the seam a test drives those failures
through. Fill in `MkdirAll` or `WriteFile` alone and the rest comes from the
`os` package, the way `trace.Filesystem` works for a recording; there is no
package-level seam and no global.

### Keeping the temporary directories

`verify` and `replay` accept `--keep-temp`, and `GOATEST_KEEP_TEMP=1` or `true`
asks for the same, so a job that cannot change a command line can still ask. An
unset, empty, `0`, or `false` variable asks for nothing, which is how a nested
job neutralizes a setting it inherited, and any other value becomes
`--keep-temp=VALUE`, which the command layer refuses with `--keep-temp takes no
value` rather than guessing which of keeping and removing was meant. The
variable is read in `cmd/goatest` alone, where it becomes the flag the command
layer parses, on the same terms as `GOATEST_TRACE`.

The flag becomes `assure.Options.KeepTemp`, which the run passes on to
`RepositoryValidatorOptions.KeepTemp`. `internal/assure/keep_temp.go` holds both
release points:

| Directory | Made | Kept as |
| --- | --- | --- |
| the baseline scratch of a round, `goatest-baseline-*` | once per round, under `TempDirectory` or the system temporary directory | `artifact` event `baseline-scratch` |
| the tree a generated candidate is validated in, `goatest-candidate-*` | once per `OriginalStable`, `Kills`, or `Suite` check of the repository validator | `artifact` event `candidate-tree` |

Keeping is the whole of the change: the directory stays where it was made, and
the `artifact` event is the run's account of having left it there. That account
is also the only place a kept path is named. With `--trace` it is in the
recording; on a failure it reaches `preserved-paths.txt` through the recording
in memory. A successful, untraced `--keep-temp` run therefore leaves directories
only the temporary directory itself lists, and nothing removes any of them
afterwards.

Three things are not kept. The mutation workspace's snapshot belongs to
go-mutants, which creates and removes it; see [limitations](limitations.md). The
fuzz cache an original-control execution makes for a `-test.fuzz` argument is
removed when that command returns. And a test's own fixtures are removed by
`t.TempDir()` when the test ends, because `--keep-temp` is an option of the tool
rather than of the suite: a test that drives a real one points `TempDirectory`
at a `t.TempDir()`, so the framework still removes what the run kept, and
asserts on the recording and on the directory it names instead.
`TestKeepTempLeavesTheBaselineScratchOfARealRunWhereItSaysItDid` is the
reference for that, and `TestKeepTempReachesTheRunAndWhatItKeptReachesTheBundle`
for the path from a kept directory to `preserved-paths.txt`.

`--keep-temp` takes no part in `modeIdentity`, in the assurance inputs, or in
the evidence digest, and `TestKeepTempTakesNoPartInCacheIdentity` pins that
beside the same test for `--trace`. A debugging aid that changed what a run
decides would be worse than no aid at all.

## Seam policy

Behaviour a test replaces is passed to the call that uses it. A package-level
`var readCacheFile = os.ReadFile` that a test overwrites is a *seam*, and the
repository is moving away from them: one seam holds every test in its package
serial, because a test that writes it owns the package while it runs, and the
external `package cache_test` tests share the binary with the internal ones.
The rule and the reasoning are [ADR 0001](adr/0001-seam-policy.md); what
follows is how to work with it.

`internal/testkit`'s scripted fakes are not affected. A collaborator with state
or several methods stays an interface — `CommandWorkspace`, `MutationSession` —
and hooks are for the leaf operations a single call performs.

### The ratchet

`internal/devgates` holds the gate. It carries no production code: it parses
every production `.go` file with `go/ast`, collects the package-level seams,
and compares them against `internal/devgates/seam_allowlist.txt`, the ledger of
the ones the repository still has. Run it alone with:

```console
go test ./internal/devgates/
```

`mise run test` runs it with the rest of the suite, so CI does too.

A `var` counts as a seam when its declared type is a function type, or when it
has no declared type and its value is a function literal, a package-level
function, a selector qualified by an import (`os.Remove`), or a composite
literal of a package-local struct whose every field is a function. Data is not:
a `//go:embed` var, a sentinel from `errors.New` or `fmt.Errorf`, a basic
literal, a slice or map literal, a zero-valued mutex, `sync.Once`, or atomic.
Test files are never scanned, build constraints are never applied — a
Windows-only indirection counts on Linux too — and `dist`, `reports`,
`testdata`, `vendor`, and dot directories are skipped.

The scan and the ledger must agree exactly, in both directions.

**A seam the ledger does not name** fails
`TestPackageLevelSeamsMatchAllowlist`, printing the offending declarations in
ledger format. It means a new global arrived. The fix is to not introduce it —
write the [hooks](#writing-hooks) below instead. Pasting the printed line into
the ledger is a reviewed exception to argue in the pull request, not the
routine way to a green gate.

**A ledger entry the tree no longer has** fails the same test from the other
side: a seam went away without the ledger being updated. Delete its line in the
commit that removed it, so the shrink is reviewed with the change that earned
it. Deleting a line ahead of the seam it names fails the gate from the first
side instead, so the ledger and the tree move together or not at all.

`TestSeamAllowlistIsSortedAndFreeOfDuplicates` keeps the file itself readable
as a diff: entries are `package-path name`, sorted by package and then by name,
with no duplicates. Lines opening with `#` are comments.

### Writing hooks

Three pieces, taking `internal/cache` as the worked example. First, one
immutable struct per concern in the package's `hooks.go`, with a `resolved()`
that fills every unset field from the real implementation:

```go
type storeHooks struct {
	// read reads a stored report.
	read func(path string) ([]byte, error)
	// ... one field per operation the calls perform
}

func (hooks storeHooks) resolved() storeHooks {
	if hooks.read == nil {
		hooks.read = os.ReadFile
	}
	// ...
	return hooks
}
```

Second, the exported function keeps its signature and delegates to an
unexported `xxxWithHooks` that resolves once, at the top:

```go
func (store *Store) Get(digest string) (report.Report, bool, error) {
	return store.getWithHooks(digest, storeHooks{})
}

// getWithHooks is Get against a filesystem the caller supplies.
func (store *Store) getWithHooks(digest string, hooks storeHooks) (report.Report, bool, error) {
	hooks = hooks.resolved()
	// ...
}
```

Third, a test fills in the one operation it drives and passes the value to the
call. Nothing is installed, nothing is restored, and the test is parallel:

```go
func TestGetReturnsTheReadFailureWithoutDecodingFallbackBytes(t *testing.T) {
	t.Parallel()
	failure := errors.New("read failure")
	hooks := storeHooks{
		read: func(string) ([]byte, error) { return []byte(`{"schema":"assurance-report-v1"}`), failure },
	}
	got, ok, err := New(t.TempDir()).getWithHooks("digest-a", hooks)
	if !errors.Is(err, failure) || ok || !reflect.DeepEqual(got, report.Report{}) {
		t.Fatalf("Get = %+v, ok %v, err %v", got, ok, err)
	}
}
```

Four details decide whether the shape holds up:

- **Resolve at the entry point and pass the resolved value down.**
  `saveGraphWithHooks` resolves once and hands the same `graphHooks` to
  `Graph.jsonWithHooks`, so one call sees one filesystem. `resolved()` is
  idempotent, so a helper that both an exported function and another
  `xxxWithHooks` call — `Graph.jsonWithHooks` is one — may resolve again
  without changing what a caller supplied.
- **A default that reads another hook must snapshot what it needs.**
  `scanHooks.digestFile` defaults to digesting through the resolved `open`
  hook, so `resolved()` builds `digestThrough := scanHooks{open: hooks.open}`
  and closes over that — never over `hooks`, the field it is about to fill.
- **A real implementation returning a concrete type needs a small interface.**
  `os.CreateTemp` returns `*os.File`, so the field is
  `createTemporary func(directory, pattern string) (cacheWritableFile, error)`
  and the default wraps the call. `cacheWritableFile` names only the four
  methods the write path uses, which is also all a test's stub has to provide.
- **A default that is not a plain `os` call needs a test that reaches it
  through the zero value.** `storeHooks.collect` defaults to `collectUnlocked`
  rather than the exported `Collect`, because the commit that calls it already
  holds the write lock; every other test replaces the collector, so
  `TestPutTrimsTheBoundedCacheAfterCommittingAnEntry` drives a real policy
  store and asserts on the directory instead. Without it, a wrong default is
  invisible until production deadlocks.

Where the hooks belong to an exported constructor or a long-lived service, they
are an exported argument or field rather than an internal struct:
`trace.NewDirSink(root, run, hooks)` takes a `trace.Filesystem`, and
`app.Service` carries `TraceFilesystem` and `DiagnosticsFilesystem`. The
`resolved()` contract is identical.

### Migrating a package

A package moves when a change reaches it, not on a migration branch, in three
commits:

1. **Pin what the seams left implicit.** The tests that replace an operation
   never exercise the default behind it, so start with the test that does, and
   make it pass against the unmodified code.
   `TestPutTrimsTheBoundedCacheAfterCommittingAnEntry` is the reference: it is
   the net the refactor lands on.
2. **Introduce the hooks and shrink the ledger in one diff.** Add `hooks.go`,
   route the exported functions through `xxxWithHooks`, delete the `var` block,
   rewrite each fault-injection test to pass its hooks, and delete the same
   seams' lines from `seam_allowlist.txt`. Take the package's seams in one go:
   a single one left behind keeps the whole package serial, so a partial move
   buys nothing.
3. **Turn the suite parallel.** Add `t.Parallel()` to the top-level tests and
   to the subtest bodies, and delete the `installXxx`/`t.Cleanup` restore
   helper the seams needed. Serial exceptions are fine when a test asserts on
   shared state after a loop, but say why in a comment.

Rewrite the fault injection one-for-one: every stage the old test drove must
still be driven, or the refactor traded coverage for parallelism. Check the
result by mutation — break a hook in production and confirm the test that
should catch it does — and finish with a repeated race run of the package
(`-race -count=4`) and `go test ./internal/devgates/`.

## Profiling

Not yet implemented. There is no benchmark or performance contract for the
runner; see [limitations](limitations.md).
