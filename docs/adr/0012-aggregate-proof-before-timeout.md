# 0012 — Exact compatible-group mutation proofs

## Status

Accepted, 2026-09-05. Revised, 2026-09-06. Implemented by the mutation
execution planner in `internal/assure`.

## Context

A mutation kill is existential: one completed failing execution against a
passing exact original proves the mutant is killed. Survival is universal over
the reaching targets: every non-discharged target must pass with the mutant
active. Starting one process per target repeatedly pays test-binary startup,
`TestMain`, resource, and suite setup.

Cost-bounded individual prefixes, duration-bounded batches, selector byte
limits, and recursive attribution splits attempted to find a named killer
early. Their thresholds were policy guesses rather than proof boundaries. They
made command count and worst-case time depend on constants unrelated to the
program, and a completed aggregate kill needed no narrower confirmation.

## Decision

1. Reaching targets are partitioned only by their actual execution identity:
   package and environment. Every compatible group has one exact `-test.run`
   selector containing its targets in canonical identity order. Groups are
   scheduled by summed measured duration, then execution identity. This order
   may find a kill sooner but never changes a partition or verdict.
2. Every compatible group runs at most once. There is no individual witness
   prefix, duration or target-count boundary, selector-size estimate,
   refinement, bisection, or attribution retry.
3. A passing group proves survival for every named target. A failing group is a
   terminal kill proof for the exact selected set. The evidence store records
   every selected target's behaviour key and `whole_tree` boundary.
4. A historical kill is reusable when its complete stored witness is contained
   in one current compatible group, every witness target still reaches and
   passed its clean control, and every behaviour key and boundary is unchanged.
   Newly reaching targets do not invalidate an existing existential proof.
5. Timeout, unavailable-control, and inconclusive results make only their group
   unknown. Later groups still run and any completed kill decides the mutant.
   If no group kills, unknown observations are combined in plan order into one
   inconclusive result. Survival requires every group to pass.
6. Aggregate budgets sum the applicable distinct same-run clean observations
   with saturating arithmetic and the user containment ceiling. No fixed margin
   or multiplier participates.
7. The planner does not guess a host argv limit. An operating-system rejection
   of an exceptionally large exact selector is an explicit infrastructure
   error and establishes no mutation verdict.

## Soundness

Every stored kill names the exact set that ran in one package and environment.
Its exact original passed and the mutated execution completed with failure.
Reusing that conjunction of unchanged target behaviour keys preserves the same
existential witness even when unrelated reaching targets are added. No timeout,
incomplete execution, or scheduling estimate is admitted as evidence.

Every survival follows only after every compatible group completed with a pass.
Discharged targets have separate coverage or infection proofs. An unknown group
therefore prevents survival but cannot erase a completed kill from another
group. Infrastructure and protocol errors and context cancellation abort the
run rather than manufacture a result.

## Consequences

- Cold command count is bounded by compatible execution environments per
  mutant, not by reaching target count or heuristic batch boundaries.
- Cross-test interactions remain attributable to the exact selected set and
  are reusable without extra mutant executions.
- Reports do not promise an individual killer when only an aggregate proved the
  result.
- The process API remains the only selector-size boundary, so behavior does not
  vary with a guessed constant.
