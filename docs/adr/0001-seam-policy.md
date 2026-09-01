# 0001 — Test seams are arguments, not package-level variables

## Status

Accepted, 2026-09-01. Enforced by `internal/devgates`. The shape it prefers is
implemented by `internal/evidence/hooks.go`, `internal/cache/hooks.go`,
`trace.Filesystem`, `app.Service.DiagnosticsFilesystem`, and
`assure.runDependencies`.

## Context

goatest tests the parts of itself that touch the filesystem, the process table,
and the toolchain by replacing the operation under test. For most of the
repository's history the way to do that was a package-level variable:

```go
var readCacheFile = os.ReadFile
```

Production reads it, a test overwrites it, and `t.Cleanup` puts the original
back. It is the smallest edit that makes a read failure reachable, and 102 of
them accumulated across eleven packages.

The variable is shared by everything in the package, and the external
`package cache_test` tests share the test binary with the internal ones, so a
test that writes one owns the package for as long as it runs. Three costs
follow.

No test in such a package may call `t.Parallel()`, whether or not it touches a
seam. `internal/evidence` and `internal/cache` were entirely serial for the
sake of the handful of tests that injected a fault, and the packages with the
most seams are the packages whose tests drive real processes and real files —
the slow ones.

The coupling is invisible where it matters. `Put`'s signature does not say that
it reads a package variable, so nothing tells a reader what a test would have
to install to change its behaviour, and nothing stops a second test from
installing something else at the same time.

The restore is the test's own obligation, discharged far from the place that
depends on it. A `t.Cleanup` that is forgotten, or a helper like
`installEvidenceHooks` that grows a field its restore does not cover, leaks one
test's injected fault into the next one, and the failure then surfaces
somewhere other than the test that caused it.

The alternative was already in the repository in three places, arrived at
independently while building recent features. `trace.NewDirSink(root, run,
hooks)` takes the filesystem it writes through as an ordinary argument.
`app.Service` carries `TraceFilesystem` and `DiagnosticsFilesystem` as fields
whose zero value is the `os` package. `assure.runWithDependencies` takes the
whole table of a run's collaborators as a `runDependencies` value. None of the
three is a global, and all three are used by parallel tests.

What was missing was a rule saying which of the two shapes new code uses, and
something that notices when it does not.

## Decision

Behaviour a test replaces travels as an argument. Concretely:

1. **The exported API does not change.** `Scan`, `SaveGraph`, `(*Store).Get`
   and `(*Store).Put` keep their signatures and delegate to an unexported
   `xxxWithHooks(args, hooks)` that takes the operations it performs. Callers
   outside the package are unaffected, and the injection point stays out of the
   public contract.
2. **The hooks are one immutable struct of function fields per concern**,
   declared in the package's `hooks.go` — `scanHooks` and `graphHooks` in
   `internal/evidence`, `storeHooks` in `internal/cache`. It is a value, passed
   by value. It is never a package-level variable, so there is nothing for a
   test to overwrite and nothing to restore.
3. **The zero value is production.** A `resolved()` method fills every unset
   field from `os`, from `encoding/json`, or from the package's own function,
   so production calls `getWithHooks(digest, storeHooks{})` and a test fills in
   only the operation it drives, keeping the real behaviour for the rest. The
   default is written once, in code, instead of being restored once per test.
4. **Tests pass hooks; they never install them.** What a test supplies is
   reachable only from the call it passed it to, which is what lets the whole
   package run under `t.Parallel()`.
5. **Collaborators keep their interfaces.** A hooks struct is for the leaf
   operations one call performs. A collaborator with state or several related
   methods stays an interface: `assure.CommandWorkspace` and
   `assure.MutationSession` are answered by `internal/testkit`, and
   [ADR 0003](0003-no-replay-engine.md) rests on their staying that way.
   `runDependencies` is this same decision at the altitude of a whole run — a
   table of the run's collaborators, passed to `runWithDependencies` rather
   than read from package scope. Hooks, dependency table, and interface differ
   in grain; all three are arguments, and none of them is a global.
6. **The rule is enforced as a ratchet, not as a cleanup.**
   `internal/devgates` parses every production file with `go/ast` and compares
   the package-level seams it finds against `internal/devgates/seam_allowlist.txt`.
   The scan and the ledger must agree exactly: a seam the ledger does not name
   fails the gate as a new global, and a ledger entry the tree no longer has
   fails it as a removal that was not recorded. Agreement is all the gate
   reads, so a seam and the ledger line naming it are green when they arrive
   together; what refuses that pair is the next point, and a reviewer, not the
   test.
7. **The ledger grows only by amending this record.** The ledger's own header
   and the gate's failure message state the rule unqualified — it may shrink
   and never grow — and this record is the one place an addition is allowed. A
   pull request may add a line only when a single commit carries three things
   together: the seam, its line in the ledger, and an entry under
   [Exceptions](#exceptions) naming that line, why the behaviour cannot travel
   as an argument, and the date by which the seam goes. An addition missing
   any of the three is the growth the ratchet refuses, however green the gate
   is, and the review rejects it.
8. **Migration is per package, in the pull request that touches it.** There is
   no flag day and no branch that converts everything. A package moves when a
   change reaches it: a test-first commit that pins what the seams left
   implicit and passes against the old code, a refactor commit that introduces
   the hooks and deletes the ledger lines in the same diff, and a commit that
   turns the package's suite parallel.

`internal/evidence` (12 seams) and `internal/cache` (6) have moved. Eighty-four
remain across nine packages, thirty of them in `internal/assure`.

## Consequences

- A package's tests become parallel the moment its last seam goes, and not
  before: one remaining global holds the whole package serial. That makes the
  ledger a per-package unit of work rather than a count to shave, and it is why
  a migration commit deletes every line of a package at once.
- The gate reads shape, not use. A package-level `var` of function type, or one
  initialized from a function, is counted whether or not any test replaces it,
  and build constraints are not applied, so a Windows-only indirection in
  `internal/processtree/tree_windows.go` costs a line on every platform. The
  ledger therefore measures the shape the repository is moving away from, not
  the number of tests that currently depend on it.
- The gate cannot tell an exception from an ordinary addition. A seam and the
  line that names it agree with each other, so the test is green whichever one
  it is, and the failure message says as much before a printed line is pasted
  in: adding a line to the ledger is a reviewed exception, not the fix. What
  separates the two is the amendment decision 7 requires, which puts the reason
  and the expiry in front of a reviewer instead of leaving them in the package
  where nobody will see them.
- A default that is not a plain `os` call is a place a bug can hide.
  `storeHooks.collect` defaults to the unlocked collector rather than the
  exported `Collect`, because the commit that calls it already holds the write
  lock. Every test that injects a fault replaces that hook, so only a test
  driving the exported API reaches the default at all, and without one a wrong
  default stays invisible until production deadlocks. Every non-trivial default
  carries the same obligation.
- Fault-injection tests get longer and more explicit. A test names the internal
  call it drives — `fileDigestWithHooks`, `putWithHooks` — instead of the
  public function, and builds the hooks it needs at the call. That is the price
  of the injection point being visible in the signature, and it is what the
  removed `installEvidenceHooks`-style helpers were hiding.
- The migration has no deadline. A package nobody touches keeps its seams
  indefinitely, and the ledger is the only measure of progress. The decision
  this record makes is about direction and about what the gate refuses, not
  about when the count reaches zero.

## Exceptions

The ledger lines added under decision 7, one entry each: the `package-path
name` of the line, why the behaviour cannot travel as an argument, and the date
by which the seam goes.

None. Every line in `internal/devgates/seam_allowlist.txt` predates this record
and is covered by the migration, not by an exception.
