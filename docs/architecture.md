# Architecture

goatest is an orchestrator, not a replacement testing framework. Its core
pipeline is:

```text
CLI/config
   │
   ├─ exact repository + dependency + environment identity
   ├─ native build/vet/test/test2json baseline
   ├─ top-level coverage graph ── changeset routing
   ├─ resource leases
   ├─ race checks
   ├─ go-mutants catalog + infection probe pass
   ├─ block-routed mutant execution + paired confirmation
   ├─ targeted native fuzz / generation candidate validation
   └─ report v1 + exact cache/checkpoint
```

The `internal/assure` package coordinates a round. `internal/golang` discovers
native targets and coverage, `internal/mutationbridge` freezes the external
go-mutants contract, and `internal/evidence` creates content identities and the
impact graph. Providers run as subprocesses behind strict JSON protocols; core
contains no network client or LLM SDK.

Mutation routing reads the baseline coverage at block granularity: a mutant is
run by the targets whose executed blocks contain its start position, cheapest
target first. A position that cannot be placed in a block widens back to every
target that executed the file, and a position no target executed is left to its
package suite. Where go-mutants proves a mutation can only narrow the condition
of a branch, the targets that never entered the body that branch gates are
discharged from the reaching set instead of executed. See
[the assurance contract](assurance-contract.md) for the rule
and [trace v1](trace-v1.md) for how each decision is recorded.

Between preparing the catalog and executing it, a `probe` phase measures
infection. go-mutants builds a second instrumented tree — the program the user
wrote, with no mutant ever active, in which each site it has a probe form for
records without side effects whether the mutated value would have differed —
and the pass runs each baseline test and example target against it once, at
about the cost of one baseline. What comes back, per target, is the set of
mutants that target made differ: a mutant a measured target left out is one
that target can never distinguish from the original program, whatever else it
executes. Fuzz targets are not probed, because the mutation phase fuzzes beyond
the seed corpus a probe would measure. A measured target that left a probed
mutant out of its infections is discharged from that mutant's reaching set with
reason `never-infected`; everything the measurement does not cover — an unprobed
mutant, an unmeasured target, a fuzz target, a route the blocks could not
decide — is kept. The facts are kept in memory beside the target's coverage and
recorded in the trace so the layer stays auditable offline against a full run's
kills. Replaying one mutant skips the pass entirely and routes as it did before
the pass existed, which only executes more.

Across runs, the mutation phase keeps a store of the kills it confirmed through
one named target, `.goatest/cache/mutation-evidence-v1.json`, read once before
the phase and written once after it. A full run — the whole project, in a
first round — resolves a mutant from that store when the recorded killer still
reaches the mutant, has the same behaviour key — an allowlist over the digests
the run already computed for its snapshot: the test binary's package closure,
the data and embedded files beside it, manifests, dependencies, toolchain,
platform, environment, contract, arguments, tags, timeouts, versions — and
passed in this run's own baseline, which is the fresh control. Every other
record executes, and a record about a mutant the catalogue no longer names is
pruned when the store is written back. The report marks each reused
disposition with its provenance and the trace records the reuse as a route
with no execution beside it. The rule is in
[the assurance contract](assurance-contract.md).

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
pinned by a test that names every command goatest issues. See
[ADR 0005](adr/0005-build-cache-goatest-owns.md) and
[configuration](configuration.md) for the bound and the location.

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
and never acts as evidence or advances a latest-report index. See
[checkpoint v1](checkpoint-v1.md).

The current implementation supports one main Go module per run. Detecting
multiple main modules causes an error rather than an aggregate that could omit
work. See [limitations](limitations.md).
