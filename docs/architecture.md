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
   ├─ go-mutants catalog + execution + paired confirmation
   ├─ targeted native fuzz / generation candidate validation
   └─ report v1 + exact cache/checkpoint
```

The `internal/assure` package coordinates a round. `internal/golang` discovers
native targets and coverage, `internal/mutationbridge` freezes the external
go-mutants contract, and `internal/evidence` creates content identities and the
impact graph. Providers run as subprocesses behind strict JSON protocols; core
contains no network client or LLM SDK.

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
