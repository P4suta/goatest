# 0008 — Controls before timeouts

## Status

Accepted, 2026-09-05; amended by
[ADR 0010](0010-whole-suite-reach-before-fallback.md). Its timeout
classification and calibration are superseded by
[ADR 0018](0018-confirm-comparative-watchdogs.md). Implemented by target,
coverage-suite, infection-suite, and exact original controls; probe-recovered
routing; and control-relative mutation deadlines in `internal/assure`.

## Context

A cold dogfood run restored 1,515 completed mutants before interruption. Of
its 192 timeout findings, 175 were package-suite executions of mutants no
coverage target reached. The packages' original suites completed in roughly 14
and 23 seconds, but those mutant requests had no baseline duration and therefore
received the fixed 30-second minimum. Under concurrent load, a healthy suite
could cross that line. Every such mutant then paid the entire line and produced
no verdict. Faster warm reuse did not make this acceptable: the first run was
still hours of repeated uncertainty.

A finite process deadline is necessary. A mutation can introduce an infinite
loop, deadlock, or unbounded recursion, and a verifier cannot wait forever to
learn that. The mistake was giving that safety mechanism two jobs: the absolute
last bound on a child process and the ordinary definition of “this mutant has
stalled.” A fixed duration knows nothing about the project or the machine load
of this run, so it is too long for a millisecond unit test and too short for a
healthy 23-second suite.

The same probe tree used for infection routing is the original program with
semantics-preserving observations. Its executions are controls measured on the
same prepared session, machine, job count, arguments, and phase as the mutant
executions. A whole-suite coverage control can answer every operator at an
instrumented but uncovered position, while an infection-suite control can
answer a probed mutation that reached its site without changing the value.
Before any unresolved mutant fallback, an already-compiled semantic-original
execution can answer whether the command is healthy and provide its closest
duration without making compilation part of the deadline.

## Decision

1. **A timeout is a hard safety ceiling, not the routine wait.** The contract
   `[execution].timeout` remains the finite cancellation bound. The ordinary
   mutant budget is derived from controls in the same run. Expiration says only
   that verification stopped waiting; it is never classified as mutant
   behaviour.
2. **Target mutants use distinct clean observations.** The passing coverage
   baseline and a separately executed target probe may both contribute. A
   combined baseline-and-probe execution contributes once. Their positive
   durations are added with saturation and capped by the user containment
   timeout.
3. **Every selected package gets one coverage-suite control.** Its initial
   deadline is derived from the sum of that package's passing target baselines.
   An exact instrumented-but-uncovered position discharges the fallback for all
   operators. Only packages with an unresolved probed mutant then get an
   infection-suite control; once measured, either suite duration calibrates the
   remaining fallback.
4. **The semantic original answers before a mutant executes.** The same
   package, arguments, and merged environment run once through the
   already-prepared probe tree with no mutant active. Failure or expiration
   makes all mutants sharing it inconclusive without executing them. A positive
   passing duration joins the other distinct clean observations. Replay, which
   intentionally prepares no probe tree, retains a lazy pristine-workspace
   fallback.
5. **Positive facts override negative silence.** A positive target probe may
   restore a target coverage missed. A positive suite infection defeats a
   negative coverage observation. If a measured suite does not infect a probed
   mutant, goatest records the counterfactual surviving verdict and executes no
   mutant process.
6. **Missing facts fail closed.** An unmeasured control supplies no timing or
   routing fact. Without an exact original control and a positive duration, no
   mutant starts.
7. **Expiration never becomes a kill or survival.** ADR 0018 defines the
   immediate inconclusive result and forbids timeout retries and durable timeout
   evidence.

## Consequences

- A healthy package suite is no longer misclassified merely because its normal
  duration is near a project-independent floor.
- A non-returning target stops after one budget derived wholly from its
  same-run clean observations, while a naturally slow suite receives a budget
  derived from its own observations.
- The cold cost of an uncovered package is one coverage control, an infection
  control only when it can still answer something, one memoized prepared
  original preflight when execution remains, and mutant suites only where no
  proof answers. It pays no second-workspace compilation and is no longer one
  fixed wait per uncovered mutant.
- Runtime variation represented by distinct clean observations widens the
  budget. Variation after those observations may still create an inconclusive
  timeout, which is fail-closed and visible.
- There remains an absolute worst-case deadline: no finite observation can
  distinguish every slow computation from nontermination, so a verifier that
  supervises arbitrary mutated code needs a cancellation boundary. It is now
  exceptional safety policy, not the expected price attached to every mutant.
