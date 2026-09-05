# 0012 — Aggregate proof before timeout

## Status

Accepted, 2026-09-05. Implemented by the mutation execution planner in
`internal/assure`.

## Context

Mutation routing first runs the cheapest reaching targets individually. Most
kills are found there. A survivor, however, must establish the universal claim
that every remaining reaching target passed with the mutant active. The first
planner grouped only targets whose isolated baseline durations summed to at
most one second. That rule came from the period when a fixed deadline also
served as the ordinary stall detector: isolating every slow target reduced the
ambiguity of an expiration.

Same-run controls and comparative deadlines now perform that job. The
one-second grouping limit no longer protects a verdict. Instead it repeatedly
starts the same prepared test binary and repeats `TestMain`, process setup, and
suite setup for every slow reaching target. A dogfood trace contained 139,267
planned non-fuzz executions. Replanning the recorded inputs with this decision
produces 82,353 commands: 56,914 fewer (40.9%) across 4,326 routes, with the
largest route reduced from 102 to 43 commands. Individual commands fall from
103,642 to 60,013. This calculation reuses recorded coverage and control
durations and executes no mutant.

A bounded dogfood run of an earlier, more conservative form of the aggregation
completed the same 785 mutant IDs with 2,020 commands and 325.00 seconds of
summed command time, versus 2,272 commands and 634.35 seconds for the old
planner. The last comparable command arrived 86.54 seconds after mutation
execution began instead of 266.85 seconds. Separately, an expensive survivor's
26-target isolated tail cost 133.84 seconds; the exact same selector completed
in 16.90 seconds. These are bounded comparisons, not a claim about a complete
cold run.

## Decision

1. **Keep a cost-bounded prefix individual.** Reaching targets are already in
   cheapest-first order. At least one and at most eight run individually, and
   another is admitted only while their cumulative semantic-original probe
   cost remains within two seconds. Baseline duration is used when no probe
   duration exists. A kill there remains cheap, attributable, and reusable.
2. **Retain cheap batches, aggregate the singleton tail.** After the prefix,
   targets continue to form batches whose measured probe costs sum to at most
   one second. Consecutive singleton batches left by that duration boundary
   share one exact `-test.run` selector when their package and environment are
   equal. A selector is split at 64 target names or 8 KiB of argument text.
   These bounds decide scheduling only; they never omit work or decide a
   verdict.
3. **Aggregate ambiguity is control flow, not a finding.** If the combined
   selector expires or returns another non-decisive outcome, goatest discards
   that ambiguous attempt and bisects the exact same target set. Passing halves
   prove their members together; an ambiguous half is split again. Only an
   individual target can own a terminal target-timeout or inconclusive finding.
   Thus a speculative optimisation cannot weaken the result or expand
   immediately into a linear worst case.
4. **Use the shortest measured attempt deadline.** An aggregate compares both
   the sum of the exact target controls and any measured whole-package
   controls. The shorter comparative deadline wins. The package duration is
   only a scheduling hint: because expiration refines to exact targets, it is
   not a premise of a verdict and may safely be conservative.
5. **Completed aggregates are exact evidence.** A passing aggregate actually
   ran every named target with the mutant active and proves the same universal
   claim as isolated executions. A failing aggregate is recursively bisected
   to seek a named individual killer or the smallest interacting group. If
   both smaller selectors pass, time out, or cannot provide an attributable
   outcome, the parent aggregate is checked against its exact original control
   and repeated so that a cross-test interaction kill is retained. Targets are
   never aggregated across packages or environments.

## Consequences

- The ordinary cold survivor path pays process and suite setup once per
  compatible group instead of once per slow target.
- A machine on which aggregation is unexpectedly slow automatically narrows
  the selector; users choose no tuning parameter.
- Most tail kills are narrowed to a named target and remain reusable. Only a
  genuine cross-test interaction stays attached to an aggregate and therefore
  cannot be stored as a target-specific kill.
- Arbitrary mutated code can still fail to terminate. No finite verifier can
  decide that in general, so an individual execution retains a finite safety
  boundary. Expiration remains explicitly inconclusive and never manufactures
  a kill or survival.
