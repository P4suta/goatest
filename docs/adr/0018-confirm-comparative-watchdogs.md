# 0018 — Fail closed at comparative watchdogs

## Status

Accepted, 2026-09-06. Supersedes the timeout calibration and classification in
[ADR 0008](0008-controls-before-timeouts.md).

## Context

A mutation may introduce a loop, deadlock, unbounded recursion, or another
computation that never returns. A process therefore needs a finite containment
boundary. Expiration at that boundary cannot distinguish nontermination from a
slow terminating computation, host contention, or an unstable test.

Project-independent floors, multipliers, and contract-specific caps made cold
runs pay policy durations unrelated to their tests. Recalibration and repeated
mutant executions paid those durations without turning expiration or repetition
into a proof. Persisting the result merely made an unanswered question durable.

## Decision

1. `[execution].timeout` is the user-controlled process-containment ceiling. It
   caps every derived mutation budget and is the deadline the exact original
   control itself runs under. It is not used by itself as a routine mutant
   budget.
2. A routine mutant budget is the saturating sum of every distinct positive
   clean duration observed in the same run that describes the request. Target,
   probe, package-suite, and exact-original observations all contribute when
   available. Each execution contributes at most once. A combined baseline and
   probe execution is one observation, not two. The user containment ceiling
   caps the sum.
3. Before a mutant request, the exact original command is measured once under
   the containment ceiling, memoized by package, arguments, and environment. A
   derived budget cannot protect anything by cutting that measurement short:
   the original is a program the baseline already ran to completion, so the only
   thing a narrow control deadline can do is lose the observation the mutant
   budget is derived from and make the whole compatible group inconclusive for a
   reason that says nothing about the mutation. Failure or expiration at the
   ceiling makes that group inconclusive. A positive completed duration joins
   the prior observations to derive the mutant deadline.
4. A normal target or package-suite request needs a positive clean observation
   before its exact original control starts. A missing control, a missing prior
   observation, or a passing control with no positive duration produces
   `mutation-control-unavailable`.
5. After the exact original passes, one completed mutant execution decides that
   compatible group's comparative outcome — unless its budget expires. On
   expiration the exact original is measured once more, outside the memo. A
   second control that completes says the request is still healthy on this
   machine, so the mutant runs exactly once more, under the containment ceiling
   itself. Any derived budget is a claim about how long the work takes, and the
   first one was just falsified; the ceiling is the only bound that makes no
   such claim. A second control that fails or expires buys nothing and the group
   is inconclusive. There is no split, no post-timeout confirmation, and no
   third execution. Other compatible
   groups still run because any completed failure establishes the existential
   kill claim. If none kills, unknown groups are combined deterministically.
6. Timeout findings are never written to mutation evidence. Reusable mutation
   evidence contains only kills, survivals, and unreached verdicts.
7. A probe control is bounded the same way. A target probe sums that target's
   passing baseline duration and its package's measured coverage-suite
   duration; a package-suite probe sums that package's passing target baselines
   and the same suite duration. A probe that expires supplies no infection or
   timing fact at all, so a deadline equal to a single control would lose a
   routing observation to scheduling variation alone.
8. A completed aggregate may prove that all selected targets survived or that
   their exact combination killed the mutant. A killed aggregate is already a
   terminal comparative proof and is neither split nor retried for attribution.
   Infrastructure and protocol errors and context cancellation still abort the
   run. No timeout observation becomes a kill or survival premise.

## Soundness

Every completed kill rests on a passing exact original and a completed failing
mutant execution. Every survival rests on completed passing mutant executions.
A budget expiration establishes no program property and is represented only by
an inconclusive group observation unless another group completed a kill.
Removing timeout reuse
cannot manufacture a verdict: a later run either proves an outcome from
completed executions or remains inconclusive again.

Re-measuring after an expiration does not turn the expiration into evidence.
The premise for the second run is the second control's completed duration — a
clean observation of the same request on the machine as it is now — and never
the expiration itself. Executions per compatible group are bounded at two
however slow the machine becomes, and a group whose second control cannot be
measured runs no second execution at all.

The price is explicit: a mutation that never returns costs one derived budget
plus one containment ceiling before its group is inconclusive. That is what
`[execution].timeout` is for, and it is the only number in this rule the
operator sets. Paying it once per nonterminating mutation is the cost of never
reporting inconclusive for work the machine could have finished — which is the
answer a verifier exists to give.

The budget affects liveness, not proof validity. Summing only distinct positive
clean observations makes the bound wholly data-derived, and the one ratio in the
rule is a measurement of the same machine rather than a policy multiplier.
Saturation prevents arithmetic overflow, and the user's containment timeout
remains the final operating-system boundary. A machine can slow without limit
after any observation, so no finite rule can guarantee completion; the safe
result in that case is still inconclusive.

## Consequences

- Cold execution no longer pays a one-second bootstrap, a fixed contract cap,
  or a repeated mutant confirmation.
- A verification on a loaded machine no longer reports inconclusive for work the
  machine was simply too busy to finish inside a budget measured when it was
  idle. The second control is paid only by groups that actually expired.
- Exact original controls are shared by requests with identical execution
  identity, so their cost is amortized across mutants.
- Background load observed by the same-run exact control contributes directly
  to the mutant budget without a policy multiplier.
- Target and package-suite observations contribute together, in the probe pass
  as well as the mutation pass, so measured scheduling variation does not
  require a fixed margin or multiplier.
- A nonterminating mutant consumes one derived budget and then one containment
  ceiling, and only when a second control could be measured. A request with no
  justified budget consumes no mutant process at all.
- Timeout findings must be re-evaluated on a later run; they are not durable
  evidence and cannot make a warm run inherit an old non-answer.
