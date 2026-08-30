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

## Mutation confirmation

A mutant is `killed` only after:

1. an initial mutant execution fails;
2. the immediately preceding original-code control passes; and
3. a second execution of the same mutant/request also fails.

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
