# 0014 — Resume a complete probe phase

## Status

Accepted, 2026-09-05. Implemented by the mutation checkpoint controller.

## Context

Mutation preparation produces a semantics-preserving probe tree, then goatest
executes every eligible target and the still-needed package suites against it.
The dogfood run measures about 1,115 target controls in 25.9 seconds. Earlier
checkpoints saved terminal mutants but not this completed phase, so every
interruption paid that fixed prefix again before any saved mutant work could be
continued.

The measurements are routing state, not a verdict: absence of a mutant from a
measured infection set licenses an execution to be omitted. Saving an
unfinished set would therefore be unsound, because an absent target could be
mistaken for a target that measured no infection. Numeric infection indices
also belong to one prepared catalog; source-mutant identity alone does not bind
an index if catalog order or probe capability changes.

## Decision

1. The checkpoint writes probe state once, only after every requested target
   and package-suite control has reached a measured-or-unmeasured result.
2. Every target is present. Failed or unavailable controls are explicitly
   unmeasured and carry no duration or infection set. Fuzz seed targets are
   measured like other deterministic targets. Only measured controls may carry
   those facts.
3. Infection sets remain compact ascending `uint32` indices. A second SHA-256
   fingerprint binds them to the ordered index-to-mutant mapping and each
   mutant's executable and probe-capability flags.
4. Restore requires that fingerprint, every target identity, and the exact
   requested package-suite set to match. Unknown indices, duplicates, invalid
   durations, or a changed inventory reject the phase.
5. Probe facts and terminal mutant results are one dependency cone. Rejecting
   the probe discards every saved mutant result that may have been routed by it;
   baseline and race state remain valid.
6. A missing or incomplete phase is rerun in full. There is no per-target probe
   journal and no interpretation of missing work as negative evidence.
7. A resumed trace emits `resume-probe`, not synthetic `probe-exec` events. The
   trace therefore remains a count of commands this attempt physically ran;
   clean traces are used for offline infection-layer dogfood audits.

## Soundness

The checkpoint belongs to the same exact input digest as the continuation. That
digest covers source, tests, dependencies, toolchain, options, arguments, and
selected environment; configured runtime resources disable checkpoint reuse.
The catalog fingerprint fixes source mutation identities, and the additional
index fingerprint fixes the compact representation's meaning. Exact inventory
equality prevents a measurement from speaking for a newly added target or
suite. Most importantly, presence denotes completion of the whole phase, so no
silence caused by cancellation can become a proof of absence.

Restoration creates no stronger claim than uninterrupted execution: the first
attempt would have used this same completed observation for every remaining
mutant. Every validation failure moves in the conservative direction by
repeating work and discarding dependent results.

## Consequences

- A mutation continuation avoids about 25.9 seconds of repeated dogfood probe
  executions while still preparing the probe binaries used by exact-original
  preflights.
- The checkpoint grows with one compact infection set per measured control.
- Absence of the phase triggers a fresh complete pass.
- A resumed trace is explicit about reuse but is not a self-contained
  infection-layer audit; the interrupted attempt retains the physical probe
  records.
