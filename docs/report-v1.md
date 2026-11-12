<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

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
- the effective test arguments, build tags, mutation operators, mutation
  parallelism, command timeout, and target timeout;
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

`goatest replay` restores the selected report's requested package scope,
contract, and complete `execution` object. A report without that identity is
not replayable; replay never silently substitutes the current configuration.

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
respectively, so their sum never exceeds `executed`. Both accounting fields are
required and remain explicit when zero.

An inconclusive mutant is never evidence-reused. Timeout and other inconclusive
findings are not reusable claims. It is counted in `executed` and in
`inconclusive`, because `executed = killed + survived + inconclusive` holds
however a disposition was reached. A reused mutant that reports as `accepted`
is one whose regenerated finding this run's acceptances silenced; it is outside
`executed` altogether, so it moves no counter but `accepted`, while the flag
and the provenance stay.

`reused_killed + reused_survived` is therefore a lower bound on how many
dispositions carry `reused: true`, not a count of them: a reader wanting every
reuse counts the flags in the inventory.

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

A run stopped by a signal records itself before it exits, rather than leaving
one line on stderr - when it can. A process blocked in the kernel, or killed
outright rather than signalled, runs no code and publishes nothing; what follows
describes a run that was signalled and allowed to finish exiting. It publishes a report into `reports/runs/` in the same five
formats as any other run, writes its diagnostics bundle, and renders the report
on the terminal. Its verdict is `INSUFFICIENT`, which is what a run with missing
evidence is, and its exit code is `130` or `143` rather than the `2` that
verdict usually carries: the verdict says what the evidence supports and the
exit code says how the run ended, and a run that was stopped and a run that
finished short of its contract are different facts. The report carries an
`assurance-interrupted` limitation and a finding of kind `interrupted`.

What that report holds is the run's identity, scope, contract, configuration
digest and duration - not its measurements. A cancelled run hands back no
partial result to publish, so the record of how far it got is the diagnostics
bundle written beside it under `.goatest/diagnostics/`, which holds the events
the run had recorded: the phase that was open and the commands that had run. A
workflow that keeps `reports/` for the finished runs wants that directory too,
for the stopped ones.

The latest indexes are not moved. `report`, `explain`, `accept` and `replay`
load them when they need a run that can answer a question, and a run that
settled nothing cannot answer one; the history keeps it, which is where a reader
looking for the stopped run will go. Nor is anything published at all by a
process stopped before its run began - one still waiting for the repository
cache lock has measured nothing and has nothing to say about this repository.
