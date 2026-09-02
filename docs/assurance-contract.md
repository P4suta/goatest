# Assurance contract v1

## Meaning

An assured verdict is evidence about a named snapshot, resolved scope,
configured fault model, Go toolchain, platform, and declared execution inputs.
It is not a proof of program correctness and does not cover faults outside that
model.

`ASSURED` is reserved for a resolved `full` scope. A changeset run that remains
targeted receives `CHANGE_ASSURED`; an explicit package run receives
`SCOPE_ASSURED`. If changeset impact cannot be determined safely, the resolved
scope broadens to `full`, and the report records both requested and resolved
scope.

Replay is an operation, not a new project assurance. It returns `REPRODUCED`
when the selected finding remains observable or `RESOLVED` when it does not.

## Fault model

`standard-v1` requires:

- successful package discovery, build, vet, and native baseline execution;
- correct classification of top-level tests, fuzz seeds, examples, setup
  failures, skips, and custom test-binary arguments;
- every declared resource to become ready and shut down cleanly;
- race checks for packages selected by static concurrency and observed
  reachability (the estimate is always a report limitation);
- a complete `strong` go-mutants catalog and terminal disposition for every
  discovered mutant; and
- stable repair-candidate validation when a candidate is produced.

`deep-v1` uses the expanded operator set and exploration limits and runs the
race detector for every resolved package.

Benchmarks are not part of either correctness contract. A future performance
contract must be explicit rather than treating ordinary benchmarks as tests.

## Mutation routing

A mutant is run by the measured targets that reach it. Reach is decided by the
coverage blocks of the baseline profiles and the start position — line and
1-based byte column — the catalog reports for the mutation: a target reaches
the mutant when one of the blocks it executed contains that position.

The decision gives way to the whole file whenever the evidence cannot carry it.
A mutant with no reported position, and a position that lies in a gap between
the blocks the coverage toolchain cut, are both routed by every target that
executed the file, which is what routing did before blocks were read. A target
restored from a checkpoint carries no blocks and keeps reaching its whole file.

A position that instrumentation describes and no measured target executed
reaches nothing, and the package suite establishes its disposition, exactly as
for a mutant in a file no target covers. Such a mutant is `unreached`, not
`surviving`; both are `survived` in the mutant inventory, so the accounting
equations below are unaffected by how a mutant was routed.

### Discharging a test a branch proof rules out

A reaching set decided by block is narrowed once more where the mutation itself
carries a proof. go-mutants publishes one for an edit that can only make the
condition of an `if` or a `for` less often true, and it names the span of the
body that condition gates, from its opening brace to its closing brace.

Write C for the original condition and C′ for the mutated one. C′ implies C,
and the whole condition is inert — no effects, no possible panic, guaranteed to
terminate. So a target during which no statement of the gated body ran
evaluated C to false every time it was evaluated, evaluated C′ to false there
too, took the same branch on every evaluation, and ran identically on the two
programs. It cannot have observed the mutation. Such a target is *discharged*:
removed from the reaching set without being executed, and named in the route's
`discharged` beside the proof that removed it. A discharge changes what a run
pays, never what it concludes.

The narrowing applies only where the evidence carries it. It is attempted on a
route decided by block with no fallback, and never on one decided by file. It
requires that the body was instrumented at all — some instrumented block must
begin inside the span — because otherwise no target's silence about the body
means anything. A fuzz target is never discharged: it explores inputs beyond
the corpus its coverage was measured on, so its blocks do not bound what it
will execute. Neither is a target restored from a checkpoint, which carries no
blocks to argue with.

A mutant every reaching target was discharged for is resolved without a single
execution, and reported as a `surviving-mutant`. It is not `unreached`: the
coverage blocks reach it, and running the package suite would only run the
tests the proof has already ruled out. That no test takes the branch the
mutation narrows is the finding — a real gap in the suite, stated for the cost
of reading a coverage profile.

Both narrowings are proof layers in the sense of
[ADR 0004](adr/0004-proof-layers-not-budgets.md): an execution is removed only
where evidence the run already holds proves it could not observe the mutant,
never by a time budget, a sample, or an exclusion of slow targets, and a layer
that cannot establish its premise keeps the execution.

## Mutation confirmation

A mutant is `killed` only after:

1. an initial mutant execution fails;
2. the original-code control for that request passes; and
3. a second execution of the same mutant/request also fails.

Each distinct control command — the package, arguments, and environment of the
killing request, which the original code does not vary by mutant — runs once
per mutation phase and its outcome answers every kill that shares it. The
snapshot is frozen for the whole phase and re-verified afterwards, so a
remembered control is the same evidence as a repeated one without the repeated
cost.

An original control failure is `flaky-mutation-control`; a non-reproducing kill
is `flaky-mutation-kill`. Both are inconclusive evidence and prevent an assured
verdict. A mutation that does not compile is `compile-rejected`, never
“compile-equivalent”.

Every discovered mutant has exactly one report-v1 disposition:

```text
discovered = executed + compile-rejected + accepted + out-of-scope + unknown
executed   = killed + survived + inconclusive
selected   = executed + compile-rejected + accepted + unknown
```

The aggregate counts must exactly match the ID-level mutant inventory. Any
`unknown` disposition requires `ERROR`.

## Acceptances

An acceptance is human authorization, not a mutation result. It requires a
finding ID, non-empty reason, future RFC3339 expiry, and may carry owner and
ticket. Report v1 includes that metadata, and every mutation marked `accepted`
must reference a matching record. Expired acceptances are ignored and cannot be
persisted as evidence for a run that started after expiry.

## DEFECT, INSUFFICIENT, and ERROR

`DEFECT` means user code violated a baseline, race, build, vet, or test
contract. `INSUFFICIENT` means execution completed but a survivor, flaky or
inconclusive outcome, unpersisted fuzz kill, excluded boundary, or other
evidence gap remains. `ERROR` covers incomplete accounting and toolchain,
provider, filesystem, protocol, or workspace failures.

A limitation is always structured with a stable code. Excludes, estimates,
unavailable metadata, and skipped later phases must never be hidden behind an
assured-looking percentage.
