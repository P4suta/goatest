# Architecture

goatest is an orchestrator, not a replacement testing framework. Its core
pipeline is:

```text
CLI/config
   │
   ├─ exact repository + dependency + environment identity
   ├─ native build/vet/direct test-binary baseline
   ├─ top-level + exact package-suite coverage graph ── changeset routing
   ├─ resource leases
   ├─ race checks
   ├─ go-mutants catalog + filtered target/package-suite infection controls
   ├─ exact-original preflight + block-routed mutant execution
   ├─ deterministic fuzz seeds / generation candidate validation
   └─ report v1 + exact cache/checkpoint
```

The `internal/assure` package coordinates a round. `internal/golang` discovers
native targets and coverage, `internal/mutationbridge` freezes the external
go-mutants contract, and `internal/evidence` creates content identities and the
impact graph. Providers run as subprocesses behind strict JSON protocols; core
contains no network client or LLM SDK.

Baseline compiles one coverage binary per package, instrumenting exactly that
test binary's module import closure, and measures that package's top-level
targets with the same bounded job count used later by probes and mutants. It
then measures one exact whole-package coverage control per selected package,
parallel across packages. Whole-package commands receive the union of all
acquired resource environments; isolated targets retain their own resource
overlays. Workers publish into indexed private slots; one coordinator merges
evidence, coverage, checkpoints, and errors in target or import-path order.
Thus process time overlaps without making report bytes or error selection
depend on scheduler completion order. An exclusive configured resource reduces
the shared limit to one. The decision and its frozen-workspace boundary are in
[ADR 0009](adr/0009-parallel-measurement-serial-commit.md).

Mutation routing reads the baseline coverage at block granularity: a mutant is
run by the targets whose executed blocks contain its start position, cheapest
target first. A position that cannot be placed in a block widens back to every
target that executed the file, and a position no target executed is left to its
package suite. Where go-mutants proves a mutation can only narrow the condition
of a branch, the targets that never entered the body that branch gates are
discharged from the reaching set instead of executed. See
[the assurance contract](assurance-contract.md) for the rule
and [trace v1](trace-v1.md) for how each decision is recorded.

The whole-package coverage control first asks whether the conservative fallback
ever executes an otherwise-unreached mutant's exact start position. If that
position is instrumented and not covered, the original suite did not reach it
and activating the mutation cannot change that execution. Unknown positions,
coverage gaps, failed controls, and missing profiles narrow nothing. This
operator-independent layer, its positive-counterexample rule, and its
independent audit are [ADR 0010](adr/0010-whole-suite-reach-before-fallback.md).

Between preparing the catalog and executing it, a `probe` phase measures
infection. go-mutants builds a second instrumented tree — the program the user
wrote, with no mutant ever active, in which each site it has a probe form for
records without side effects whether the mutated value would have differed —
and the pass runs each baseline test and example target against it once. It adds
a whole-suite infection control only for a package where some probed mutant
still needs a suite fallback after coverage. What comes back is the set of
mutants each execution made differ.

The target controls narrow and repair routing. A measured target that left a
probed mutant out of its infections is discharged from a block route with reason
`never-infected`. Conversely, a positive infection adds the target when
coverage did not see it, so a subprocess or another coverage-blind execution is
not thrown away merely because its profile was silent. The package control is
the exact conservative suite execution an otherwise-unreached mutant would
receive. If that measured suite never made a probed mutant differ, activating
the mutant cannot change it and no mutant process is started. If it did differ,
its current-machine duration joins the suite coverage duration as a control for
the mutant suite's comparative deadline. A positive infection always defeats a
contradictory negative coverage observation.

Everything the measurements do not cover — an unprobed mutant or an unmeasured
execution — is kept on the conservative path. Fuzz targets execute and probe
their registered seed corpus like other deterministic targets. The facts live in memory beside coverage and are
recorded in the trace. Replaying one mutant skips the pass and routes
conservatively, which only executes more.

Ordinary mutant commands do not wait for a project-independent fixed timeout.
Their budget is the saturating sum of distinct positive same-run clean
observations for the exact request, capped by `[execution].timeout`. Applicable
target and package-suite observations all contribute, while a combined baseline
and probe contributes once. Before a mutant runs, the semantic
original executes through the already-compiled probe binaries under the
request's same-run derived deadline, memoized by package, arguments,
environment, and deadline;
a failure or expiration makes that compatible execution group inconclusive
without executing it. Other groups still run because any one may establish a
kill. Compilation is outside the derived budget. Replay uses its pristine
fallback for the same control. With no positive control, no mutant starts for
that group. A mutant timeout answers only its group and starts no recalibration,
split, or retry. The design is recorded in
[ADR 0018](adr/0018-confirm-comparative-watchdogs.md).

Within a reaching route, every target with the same package and environment
shares one exact selector. There are no cost, duration, target-count, or
selector-length scheduling thresholds. Each compatible group runs at most
once. A completed passing selector proves survival for every target it names;
a completed failing selector is a terminal kill proof for that exact target
set. No attribution split or mutant retry follows either outcome. Unknown
groups do not prevent later groups from establishing a kill. If none does, all
unknown groups become one deterministic inconclusive result; survival requires
every group to pass. Execution, API, and protocol errors and context
cancellation abort the run. The operating system may reject an exceptionally
large selector, which is an explicit infrastructure error rather than a reason
to guess a platform-dependent argv boundary. This is
[ADR 0012](adr/0012-aggregate-proof-before-timeout.md).

Across runs, the mutation phase keeps a store of what it established about each
mutant, `.goatest/cache/mutation-evidence-v1.json`, read once before the phase
and written once after it. Cache status strictly validates and accounts for
this store, policy GC retains it, and an explicit cache flush removes it along
with exact-input results so the next run executes the reusable checks again.
A full run — the whole project, in a first round —
resolves a mutant from that store on a condition of the shape its verdict has.
A kill is existential and is reused when every target in its recorded witness
set still reaches the mutant in one compatible execution group, has the same
behaviour key, and passed in this run's own baseline, which is the fresh
control. A survival is universal, so it is reused only when
every target that reaches the mutant now is one the recording run ran against
it under the same key; a mutant no target reaches is reused against the key of
its package suite. Timeouts settle nothing and are neither recorded nor reused.
The behaviour key is an
allowlist over the digests the run already computed for its snapshot: the test
binary's package closure, the data and embedded files beside it, manifests,
dependencies, toolchain, platform, the selected base environment overlaid by
the target or package-suite resource environment, contract, arguments, tags,
timeouts, the goatest version, the exact running goatest executable digest, and
the go-mutants version embedded in the running executable's Go build
information. If that version is missing or unversioned, the identity is the
exact running executable's SHA-256. Failure to obtain either stops the run
before cache lookup or evidence storage; no compiled-in version guess is used.
Every selected package that can reach a repository read the log can observe
is observed through Go's test action log; only the baseline/mutant targets that
actually escape their ordinary inputs use the whole-tree variant, with every
unknown observation conservatively widened. Static analysis forces whole-tree
keys, without an observation, for known reads outside the observable interval
or through APIs the action log cannot see. Whole-tree target and suite keys are
generated only when an observation requires them.
Every other record executes, and
a record about a mutant the catalogue no longer names is pruned
when the store is written back. The report marks each reused disposition with
its provenance and the trace records the reuse as a route with no execution
beside it. The rule is in [the assurance contract](assurance-contract.md) and
the reasoning in [ADR 0007](adr/0007-survived-evidence-is-universal.md).

Changeset routing reads two things about each top-level target: the files its
baseline run covered, and the import closure its test binary links. That
closure is the package's own transitive imports together with the imports its
test files add, each in-module test import expanded through the same `go list`
listing, so a change to a helper a target reaches only from a `_test.go` file
still re-selects that target.

Mutation candidate discovery is scoped independently from test-binary
execution. Explicit package verification discovers candidates only in those
package directories; a changed test file widens its package, while an exact
production-file change stays exact. Downstream packages selected to run tests
do not become mutation candidates merely because they execute. An unscoped run
retains repository-wide discovery. Workspace inspection builds the selected
model from one exact `go list -json` invocation. See
[ADR 0020](adr/0020-bootstrap-cold-preparation-with-verified-local-work.md).

Every go command a run starts uses cache storage goatest owns. The durable form
is served through `GOCACHEPROG` and the hidden `goatest cacheprog` subcommand,
with a base layer the machine keeps between runs and a scratch layer removed
when the run ends. Reads resolve scratch and then base. Repository inspection
and baseline or race compilation write through that protocol to the base.
Mutation preparation runs against the native projection and atomically promotes
only its new valid content-addressed actions and objects after preparation
succeeds. Test execution never persists its incidental cache writes.
Preparation runs on the mutation workspace while `go vet` and `go build` run
on a distinct pristine control workspace. Their trace spans overlap, and the
coordinator joins preparation before any probe or mutant execution.
When both owned layers miss, the external cache may read that one action from
cmd/go's host cache. It accepts the entry only after validating the native
record, identifiers, size, regular file, and complete content hash, then copies
the bytes into owned storage before replying. Every invalid or unavailable
entry is an ordinary compiler miss; a host path is never returned.
The build-only check passes the host null device to `go build -o`; this keeps
an exact selection of one `main` package from writing its executable into the
frozen repository while preserving compile-only behaviour for every selection.
That second half is what keeps the cache useful. A baseline target is the
project's compiled test binary executed directly with Go's machine-readable
test framing, and a test suite spawns go commands of its own; were a target run
to persist, every throwaway package those fixtures compile would evict the
standard library the base layer exists to hold. Direct execution avoids a
`go tool test2json` process and its child-process handoff for every isolated
target. Once every pending package binary has compiled, baseline, race, and the
prepared probe and mutant executions switch to a run-owned native `GOCACHE`
projected from the persistent base: action indexes are translated once and
immutable outputs are hard-linked, so child Go commands avoid the external
protocol hot path. Mutation preparation uses the projection without a separate
verification: the prepared baseline is the run's control, and a plan runs no
repository tests. Candidate validation retains an explicit instrumented
verification on the same projection because it has no prepared baseline.
Successful preparation promotes only the action IDs absent from the initial
projection, while failed or interrupted preparation promotes nothing. Race
binaries are first compiled to the host null device through the persistent
layer; the next native execution sees the new generation through a zero-active
refresh. A one-minute admission
barrier stops new native commands, drains the finite active batch, and collects
or refreshes only at zero active; projection or collection failure sends every
later command to external scratch and cannot change a verdict. The rules are
pinned by tests that name every command goatest issues. See
[ADR 0015](adr/0015-execute-framed-baselines-directly.md) and
[ADR 0017](adr/0017-project-controls-use-a-native-cache-projection.md), with
the cold bootstrap in
[ADR 0020](adr/0020-bootstrap-cold-preparation-with-verified-local-work.md).

All three layers are bounded, and nobody has to remember to bound them. The run
collects the base layer when it ends and the served processes keep the scratch
layer inside the same cap as they go, each under a non-blocking lock on the
layer so concurrent runs and `goatest cache gc` yield to one another instead of
duplicating the walk. A collection spares everything read within two touch
intervals, which is what makes it safe beside a live build: the go command opens
a cached file after the response that named it. goatest never adopts a directory
it did not make, so a `build_dir` pointing at anything that already holds other
files is refused rather than collected. The native projection is collected at
zero-active-command boundaries and removed with the run. See
[ADR 0005](adr/0005-build-cache-goatest-owns.md) and
[configuration](configuration.md) for the bound and the location.

Every byte a run writes outside the repository has one owned top-level
directory. Almost all of it goes below `goatest-run-*` under the configured
temporary root, holding `build/` for the external build cache layer and its
independent native `go-cache/` backing,
`baseline-*` per round and `candidate-*` per validated candidate. A native build-cache projection
is the filesystem-required exception: `goatest-native-cache-*` sits beside the
persistent base so its objects can be hard links rather than a second copy. Both
top-level forms carry an owner
pair — an advisory lock held open for the whole run, and a
`goatest-temp-owner-v1` marker naming the run, the process, the repository and
whether it was kept on purpose. The lock is the liveness signal, because a pid
wraps and is reused; a lock that can be taken means its holder is gone. Each run
sweeps both named roots before it writes there and collects what runs that were
killed left behind, and `goatest cache gc` does the same on demand. A
directory kept with `--keep-temp` is marked kept, so no sweep takes it, and is
recorded in `.goatest/kept-temp-v1.json`, which `cache status` lists and
`cache gc` collects once it is older than the cache TTL. None of this can fail
a run: it is housekeeping, and a run that could not do it still produces its
verdict. See [ADR 0006](adr/0006-every-temporary-directory-has-an-owner.md).

Verification is read-only. A generated test or corpus entry is stored through
`internal/repair` as an isolated candidate. The separate `fix --apply`
operation validates candidates against a copied repository and performs the
only authorized source/corpus mutation.

Reports are the durable boundary. Before a report can advance a latest index,
it must satisfy the scope/verdict rules, timing and toolchain requirements,
mutant inventory equations, acceptance linkage, and cache provenance checks.

One OS advisory lock covers the repository cache and the full verification
lifetime. Interrupted scheduling state is written separately as strict
`assurance-checkpoint-v1`; it can skip only exact-input, fully classified units
and never acts as evidence or advances a latest-report index. Phase boundaries
atomically replace a complete base document; completed targets, package-suite
controls, and mutants between them are individually synced to a checksummed
append-only journal. This preserves per-unit crash recovery while making
cumulative checkpoint I/O linear rather than quadratic. Every out-of-order
completed target is journaled immediately; reports reconstruct discovery order
and checkpoints sort identities, so completion timing cannot change durable
bytes. Positive target and instrumented blocks preserve exact routes, and a
completed baseline carries its compact global instrumentation and package-suite
controls so resume starts no baseline command. A complete catalog-bound probe
phase is another atomic boundary, so a mutation continuation does not repeat
its target and package-suite controls. See
[checkpoint v1](checkpoint-v1.md),
[ADR 0011](adr/0011-append-only-checkpoint-journal.md),
[ADR 0013](adr/0013-preserve-block-routing-across-resume.md),
[ADR 0014](adr/0014-resume-complete-probe-phase.md), and
[ADR 0016](adr/0016-publish-every-baseline-control.md).

The current implementation supports one main Go module per run. Detecting
multiple main modules causes an error rather than an aggregate that could omit
work. See [limitations](limitations.md).
