# Architecture

goatest is an orchestrator, not a replacement testing framework. Its core
pipeline is:

```text
CLI/config
   │
   ├─ exact repository + dependency + environment identity
   ├─ native build/vet/test/test2json baseline
   ├─ top-level + exact package-suite coverage graph ── changeset routing
   ├─ resource leases
   ├─ race checks
   ├─ go-mutants catalog + filtered target/package-suite infection controls
   ├─ block-routed mutant execution + paired confirmation
   ├─ targeted native fuzz / generation candidate validation
   └─ report v1 + exact cache/checkpoint
```

The `internal/assure` package coordinates a round. `internal/golang` discovers
native targets and coverage, `internal/mutationbridge` freezes the external
go-mutants contract, and `internal/evidence` creates content identities and the
impact graph. Providers run as subprocesses behind strict JSON protocols; core
contains no network client or LLM SDK.

Baseline compiles one coverage binary per package and measures that package's
top-level targets with the same bounded job count used later by probes and
mutants. It then measures one exact whole-package coverage control per selected
package, parallel across packages. Whole-package commands receive the union of
all acquired resource environments; isolated targets retain their own resource
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

Everything the measurements do not cover — an unprobed mutant, an unmeasured
execution, or a fuzz target — is kept on the older conservative path. Fuzz
targets are not probed because the mutation phase explores beyond the seed
corpus a probe would measure. The facts live in memory beside coverage and are
recorded in the trace. Replaying one mutant skips the pass and routes as it did
before the pass existed, which only executes more.

Ordinary mutant commands do not wait for a project-independent fixed timeout.
Their comparative deadline is derived from the slower of the same run's passing
baseline and probe controls, widened by disagreement between those samples. A
remaining package-suite fallback first runs the exact uninstrumented original
command, memoized once per package/arguments/environment identity; a failure or
expiration makes all dependent mutants inconclusive without executing them,
while a pass supplies the closest duration sample. The contract ceiling and
`[execution].timeout` remain hard cancellation caps because arbitrary mutated
code may not terminate. The legacy fixed calibration is used only when no
control fact exists, and fuzzing retains a separate bound because a seed-corpus
duration does not predict a 10,000- or 100,000-input campaign. The design
decision is recorded in
[ADR 0008](adr/0008-controls-before-timeouts.md).

Across runs, the mutation phase keeps a store of what it established about each
mutant, `.goatest/cache/mutation-evidence-v1.json`, read once before the phase
and written once after it. Cache status strictly validates and accounts for
this store, policy GC retains it, and an explicit cache flush removes it along
with exact-input results so the next run executes the reusable checks again.
A full run — the whole project, in a first round —
resolves a mutant from that store on a condition of the shape its verdict has.
A kill is existential and is reused when the recorded killer still reaches the
mutant, has the same behaviour key, and passed in this run's own baseline,
which is the fresh control. A survival is universal, so it is reused only when
every target that reaches the mutant now is one the recording run ran against
it under the same key; a mutant no target reaches is reused against the key of
its package suite, and a timeout, which settles nothing, is reused fail-closed
under the same conditions and keeps its finding. The behaviour key is an
allowlist over the digests the run already computed for its snapshot: the test
binary's package closure, the data and embedded files beside it, manifests,
dependencies, toolchain, platform, environment, contract, arguments, tags,
timeouts, and versions. Packages whose sources may enumerate a repository are
observed through Go's test action log; only the baseline/mutant targets that
actually escape those ordinary inputs use the whole-tree variant, with every
unknown observation conservatively widened. Every other record executes, and
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

Every go command a run starts is served by goatest's own build cache rather than
the toolchain's, through `GOCACHEPROG` and the hidden `goatest cacheprog`
subcommand. The cache has two layers: a base layer the machine keeps between
runs, and a scratch layer removed when the run ends. Reads resolve scratch and
then base. Writes are where the rule is: **only a command that compiles or lists
writes to the base layer** — `go vet`, `go build`, `go list`, `go version`, and
a `go test -c` — and everything that runs the project's tests writes to scratch.
That second half is what keeps the cache useful. A baseline target is the
project's test binary under `go tool test2json`, and a test suite spawns go
commands of its own; were a target run to persist, every throwaway package those
fixtures compile would evict the standard library the base layer exists to hold.
The rule lives in one function, is applied by one workspace decorator, and is
pinned by a test that names every command goatest issues.

Both layers are bounded, and nobody has to remember to bound them. The run
collects the base layer when it ends and the served processes keep the scratch
layer inside the same cap as they go, each under a non-blocking lock on the
layer so concurrent runs and `goatest cache gc` yield to one another instead of
duplicating the walk. A collection spares everything read within two touch
intervals, which is what makes it safe beside a live build: the go command opens
a cached file after the response that named it. goatest never adopts a directory
it did not make, so a `build_dir` pointing at anything that already holds other
files is refused rather than collected. See
[ADR 0005](adr/0005-build-cache-goatest-owns.md) and
[configuration](configuration.md) for the bound and the location.

Every byte a run writes outside the repository goes below one scratch directory
it makes for itself and removes when it ends: `goatest-run-*` under the
configured temporary root, holding `build/` for the build cache layer,
`baseline-*` per round, `candidate-*` per validated candidate, and
`control-fuzz-*` for a fuzzing original control. The directory carries an owner
pair — an advisory lock held open for the whole run, and a
`goatest-temp-owner-v1` marker naming the run, the process, the repository and
whether it was kept on purpose. The lock is the liveness signal, because a pid
wraps and is reused; a lock that can be taken means its holder is gone. Each run
sweeps the temporary root before it writes anything and collects what runs that
were killed left behind, and `goatest cache gc` does the same on demand. A
directory kept with `--keep-temp` is marked kept, so no sweep takes it, and is
recorded in `.goatest/kept-temp-v1.json`, which `cache status` lists and
`cache gc` collects once it is older than the cache TTL. None of this can fail
a run: it is housekeeping, and a run that could not do it still produces its
verdict. See [ADR 0006](adr/0006-every-temporary-directory-has-an-owner.md).

Verification is read-only. A killing fuzz artifact or generated test is stored
through `internal/repair` as an isolated candidate. The separate `fix --apply`
operation validates candidates against a copied repository and performs the
only authorized source/corpus mutation.

Reports are the durable boundary. Before a report can advance a latest index,
it must satisfy the scope/verdict rules, timing and toolchain requirements,
mutant inventory equations, acceptance linkage, and cache provenance checks.

One OS advisory lock covers the repository cache and the full verification
lifetime. Interrupted scheduling state is written separately as strict
`assurance-checkpoint-v1`; it can skip only exact-input, fully classified units
and never acts as evidence or advances a latest-report index. Phase boundaries
atomically replace a complete base document; completed targets and mutants
between them are individually synced to a checksummed append-only journal.
This preserves per-unit crash recovery while making cumulative checkpoint I/O
linear rather than quadratic. See [checkpoint v1](checkpoint-v1.md) and
[ADR 0011](adr/0011-append-only-checkpoint-journal.md).

The current implementation supports one main Go module per run. Detecting
multiple main modules causes an error rather than an aggregate that could omit
work. See [limitations](limitations.md).
