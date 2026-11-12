# 0016 — Publish every completed baseline control

## Status

Accepted, 2026-09-05. Revised to package-batched publication, 2026-09-06.
Implemented by the baseline schedulers, checkpoint controller, and append-only
checkpoint journal.

## Context

Baseline targets execute concurrently but used to become durable only as the
longest completed prefix of discovery order. That preserved deterministic
publication, yet coupled crash recovery to an unrelated slow target: in one
two-minute self-verification slice, 463 target processes completed and only 390
were checkpointed. Interrupting there discarded 73 complete measurements.

Package-suite coverage controls had a coarser boundary. None became durable
until every suite completed and the baseline was compacted, so an interrupted
attempt reran every successful suite. On a cold run these are precisely the
controls most likely to contain repository-reading tests and expensive child Go
builds.

Completion order is nondeterministic, but losing completed work is not required
to make output deterministic. Identity and presentation order are separate
questions.

## Decision

1. **Complete targets are published once per package.** A worker owns only its
   private command and result slot. The coordinator receives and validates each
   completed result, then writes one checkpoint after every worker started for
   that package has joined. If one target returns an infrastructure error, the
   same package checkpoint publishes every sibling that completed successfully
   before the error is returned.
2. **Every terminal package-suite control is published immediately.** A measured
   control stores exact covered and instrumented blocks, duration, and
   whole-tree observation. An unmeasured control stores only that conservative
   classification; it supplies no negative reachability fact.
3. **Checkpoint collections are sets keyed by stable identity.** A target is
   keyed by target ID and a suite by import path. Set extension, not slice
   prefix, is the journal fast path. Replay rejects duplicates or changed units
   and sorts each set once.
4. **Presentation remains canonical.** Reports reconstruct targets in discovery
   order. Checkpoints sort targets by ID and suites by import path. The earliest
   failing target in discovery order still owns the returned infrastructure
   error after all started workers join.
5. **A partial package retains at most one instrumentation anchor.** Every isolated
   target in a package executes the same already-compiled coverage binary, so
   its instrumented block set is identical. The first target in deterministic
   package order is the anchor; once it completes successfully it retains that
   set, while every other target retains only target-specific positive coverage.
   If the anchor is still pending or produces no usable profile, absence remains
   conservative. Global instrumentation can therefore be reconstructed without
   duplicating the largest profile component in every journal record.
6. **The report-facing in-memory target view retains no instrumentation copy.**
   Fresh and resumed rounds both expose the exact union through
   `BaselineResult.Instrumented`; per-target evidence retains only facts that can
   vary by target. The partial-checkpoint anchor is a recovery mechanism, not a
   second owner in the live result.

## Soundness

Only a completely classified unit is saved, under the same exact-input digest
that already binds source, dependencies, toolchain, configuration, environment,
and test arguments. A missing unit is pending and runs again. A malformed,
duplicated, changed, or inventory-mismatched unit rejects the checkpoint.

Out-of-order durability adds no negative evidence. A saved passing target is the
same completed measurement the uninterrupted coordinator would later publish.
An unmeasured suite cannot discharge a mutant; omitting it from completed
routing preserves the package-suite execution fallback. Missing partial
instrumentation makes routing wider, never narrower.

## Consequences

- Interruption loses at most the completed commands in the one package whose
  workers have not all joined. Every earlier package remains durable.
- A continuation compiles and executes only packages with a missing target or
  suite control. A completed baseline still compacts to the smaller routing
  representation.
- Journal line order remains physical completion order, but replayed checkpoint
  bytes and report bytes remain deterministic.
- One sync per completed package is the durability cost. It is independent of
  the number of targets in that package and is paid before work advances to the
  next package.
- In a measured 433-target interrupted self-verification, five package anchors
  kept the journal at 10.4 MB. Repeating the same instrumented set on every
  target would have added about 178.7 MB without establishing another fact;
  stripping the same copies from the live result avoids equivalent peak-memory
  retention.
