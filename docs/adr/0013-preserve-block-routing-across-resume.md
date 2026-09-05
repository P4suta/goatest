# 0013 — Preserve block routing across resume

## Status

Accepted, 2026-09-05. Implemented by the baseline checkpoint conversion.

## Context

A baseline profile establishes two different facts for a target: the files it
reached and the exact positive coverage blocks it ran. Earlier checkpoints
kept only the file list. That was safe because a resumed target was treated as
reaching every mutant in each file, but interruption silently changed the
planner from block routing to a much wider file approximation. It also made a
resumed target ineligible for universal survivor evidence, because the widened
set was no longer the set the recording run measured.

The original size concern assumed every instrumented block would be repeated
for every target. Only positive blocks are needed. On the 1,115-target dogfood
baseline, there are 129,595 such blocks. Adding them to an 8.7 MB checkpoint
produces a 34.5 MB document, and deterministic JSON encoding takes 0.21
seconds. Replanning the same 10,281 recorded routes with the same aggregate
planner reduces commands from 82,353 to 78,208 when those blocks are restored.

## Decision

1. Each completed baseline target stores its positive coverage grouped by
   module-relative file and exact 1-based line/byte-column span.
2. The append-only journal writes that target-local fact once. It does not
   rewrite blocks belonging to earlier targets; phase-boundary compaction
   canonicalizes file and block order.
3. Restore validates positive coordinates and non-reversed spans, then sorts
   and deduplicates before routing performs binary searches.
4. The field is optional within checkpoint v1. Absence identifies a checkpoint
   written by an older binary and restores `nil`, which retains the existing
   conservative whole-file route. A present object with an empty file array is
   an exact empty measurement, not missing evidence.
5. Once the baseline completes, its deduplicated global instrumentation and
   successful package-suite coverage controls are stored once at the phase
   boundary. Their presence lets an exact-input resume reconstruct the baseline
   without compiling or executing any baseline command. Absence in an
   unfinished or legacy checkpoint reruns the controls.
6. Probe infection facts remain a separate phase-level fact. Their complete,
   catalog-bound checkpoint and fail-closed restoration are specified by
   [ADR 0014](0014-resume-complete-probe-phase.md); this decision carries only
   baseline facts.

## Soundness

Preservation creates no new proof. The resumed run has the same exact input
digest, including source, tests, dependencies, toolchain, build options, and
test arguments. It merely supplies routing with the positive blocks the first
attempt already parsed from a passing target execution. Invalid or unavailable
checkpoint data is rejected and causes the established cold fallback. A
legacy checkpoint can only execute more because missing blocks are never read
as negative coverage.

## Consequences

- Interrupting a cold run no longer discards block precision or prevents exact
  survivor reuse for every restored target.
- Resuming after baseline completion starts no baseline compile, target, or
  package-suite command.
- Checkpoint storage grows by a measured tens of megabytes on this repository,
  while encoding remains far below one second and journal writes stay linear.
- Older checkpoints remain safely consumable. Downgraded binaries reject the
  additive field under their strict decoder and start cold rather than
  misreading it.
