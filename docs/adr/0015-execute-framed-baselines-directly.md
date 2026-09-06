# ADR 0015: Execute framed baseline targets directly

## Status

Accepted.

## Context

The baseline executes every selected top-level test, example, and fuzz seed in
isolation to obtain its exact coverage and repository-read evidence. Wrapping
each already-compiled test binary in `go tool test2json` added a second Go
process and a child-process handoff to every measurement. On goatest's own
baseline that fixed process cost was paid more than one thousand times and was
larger than many tests themselves.

The wrapper supplied two useful properties: unambiguous skip events and a `-p`
package label in the trace used by the independent proof auditor. Removing it
must preserve both properties on interrupted output.

## Decision

Run each compiled target binary directly with `-test.v=test2json`. Go's testing
package prefixes status records with byte `0x16`; goatest parses only those
framed `--- SKIP:` reports and follows cmd/test2json's rule that a marker also
ends preceding output without a newline. Unmarked user output cannot become a
skip fact.

go-mutants deliberately bounds captured command output by retaining its tail.
If that bound discarded any prefix and no retained skip record decides the
target, goatest refuses the baseline: absence of a record in an incomplete
capture is not proof that the target passed without skipping.

Package-suite controls need no skip classification and run directly without
verbose framing. The trace auditor derives a direct binary's package from the
preceding `go test -c -o <binary> <package>` record. A recording without that
provenance is unverifiable.

## Consequences

- One `go` process and one process handoff disappear from every isolated
  baseline target.
- Skip classification remains tied to Go's explicit framing rather than human
  output text.
- Truncated passing output becomes an explicit unknown instead of a possible
  false pass.
- Trace schema does not change.
