# Assurance report v1

The first public report contract is `assurance-report-v1`. This project has not
released an earlier schema, so the implementation has no legacy report reader.

## Durable layout

Each completed verification owns an immutable directory:

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

## Projections

JSON is the canonical complete model. HTML is self-contained and provides
scope/accounting/audit tables plus client-side search and section filtering.
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
