# Assurance report v1

The first public report contract is `assurance-report-v1`. This project has not
released an earlier schema, so the implementation has no legacy report reader.

## Durable layout

Each completed verification owns a directory that is immutable for as long as it
exists:

```text
reports/runs/<run-id>/
  assurance-report-v1.json
  assurance-report-v1.html
  assurance-report-v1.sarif
  assurance-report-v1.junit.xml
  assurance-report-v1.schema.json
```

`reports/latest-any.json` and `.goatest/latest-any.json` track the latest
completed run of any scope. `latest-full.json` exists in both locations and
advances only when `run_kind` is `full`. Changeset, package, and replay runs
cannot replace it. Use `goatest report --latest-full` or
`goatest report --run=<run-id>` to select explicitly.

The history is bounded. The newest `[reports] keep` runs — twenty by default —
and the runs the `latest-*` indexes point at are kept; older ones are collected
at the end of every run that holds the repository's cache lease, and by
`goatest cache gc`. Nothing ever rewrites a run
directory: a run is there in full or it is gone, and `goatest report --run` of a
collected run says so by name. Copy `reports/runs/<run-id>` elsewhere to keep one
past the bound.

## Required audit identity

A durable report must include:

- schema, run ID, run kind, verdict, contract, and snapshot identity;
- requested and resolved project/module/package/file scope;
- repository module/package inventory and explicit Git availability, commit,
  dirty state, merge base, and changed files;
- an effective configuration SHA-256;
- Go, goatest, go-mutants, OS, and architecture identity;
- RFC3339 start/finish times and duration;
- cache-derived state and source run ID when applicable;
- target, race, and mutant accounting;
- every selected baseline target with its terminal status and measured
  `duration_ms`;
- every ID-level mutant disposition;
- acceptance metadata, evidence, findings, repair candidates, and structured
  limitations.

If Git is unavailable, the report uses the explicit `available=false` state and
`unavailable` sentinels together with `git-metadata-unavailable`; an empty value
is invalid. Other metadata that cannot be reached before an infrastructure
failure is likewise represented explicitly and prevents ambiguity.

The JSON Schema rejects unknown fields and constrains every nested object. Go
validation additionally enforces arithmetic, scope/verdict, acceptance, cache,
and unavailable-metadata invariants that JSON Schema alone cannot express.

`targets` is canonically ordered by descending duration, then ascending target
ID for equal durations. A completed run may also carry `resume` with the total
attempt count and the numbers of baseline targets, race packages, and mutants
reused from an exact-input checkpoint. Those counts are audit metadata; the
restored units still contribute their ordinary evidence and accounting.

A mutant disposition may say `reused: true` with a `provenance` naming the run
that observed the verdict, in the `snapshot=<digest>` form a repair carries:
this run resolved the mutant from evidence an earlier run recorded, under the
conditions in [the assurance contract](assurance-contract.md), and executed
nothing for it. The two fields are one fact stated twice and are validated
against each other in both directions. The accounting carries the totals as
`reused_killed` and `reused_survived`; each is part of `killed` and `survived`
respectively, so their sum never exceeds `executed`. All four are optional and
omitted when zero, so a report written before evidence was ever reused still
validates.

A reused mutant that reports as `inconclusive` is a timeout an earlier run
recorded, which no counter sums: reusing one keeps a finding rather than
resolving anything. A reused mutant that reports as `accepted` is one whose
regenerated finding this run's acceptances silenced; it is outside `executed`,
so it moves no reuse counter either, while the flag and the provenance stay.

An interrupted checkpoint is not a partial report and cannot advance any
latest-report index. Its separate strict contract and deletion rules are in
[checkpoint v1](checkpoint-v1.md).

## Projections

JSON is the canonical complete model. HTML is self-contained and provides
scope/accounting/audit tables, a slowest-first target table, and client-side
search and section filtering.
SARIF carries findings and the audit model in run properties. JUnit represents
evidence as passing cases, findings as failures, and embeds core identity as
properties.

Terminal and pipe output is deterministic and escapes control characters so
provider or test output cannot forge `FINDING`, `REPAIR`, `ACCEPTANCE`, or
`LIMITATION` records.

## Exit codes

| Code | Meaning |
| ---: | --- |
| 0 | `ASSURED`, `CHANGE_ASSURED`, `SCOPE_ASSURED`, `RESOLVED`, or `COMPLETED` |
| 1 | `DEFECT` or `REPRODUCED` |
| 2 | `INSUFFICIENT` |
| 3 | `ERROR`, invalid input, or infrastructure failure |
| 130 | interrupted |
| 143 | terminated |
