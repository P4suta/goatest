# 0004 — Proof layers, not budgets

## Status

Accepted, 2026-09-02; its coverage-blind consequence was amended by
[ADR 0008](0008-controls-before-timeouts.md) and
[ADR 0010](0010-whole-suite-reach-before-fallback.md). Implemented by the block routing
of `internal/assure` (`routeMutant`, #18), the branch-never-taken discharge it performs with
go-mutants' branch proof (`dischargeNarrowedBranch`, #23), the `route` events
of `internal/trace` that name what each layer decided (#18, #19), and the
`internal/devtools/proofaudit` audit that holds every layer to recorded kills
(#20, #24).

## Context

A first verification of this repository ran for about three hours, and 98 % of
that was mutation: every mutant executed against every test that ran its file,
and a surviving mutant paying for all of them. Every mutation-testing tool
meets this wall, and the industry's answers are budgets — a time limit after
which the remaining mutants are not run, a sample of the mutants, an exclusion
list of the slow packages, a model that predicts which mutants are worth
running.

Each of those answers makes the tool faster by making its verdict weaker. A
mutant that was not run is a claim that was not tested, and a verdict that
rests on untested claims is not the fail-closed verdict goatest exists to give.
A budget is therefore not an optimisation of goatest; it is a regression of it,
however the setting is spelled.

The thesis of the project is the other way round. goatest combines techniques
so that it obtains, for every mutant, the same proof that executing it would
give — and executes a mutant only when nothing else proves the outcome. A
mutant has three ways to survive a test: the test never reaches the mutated
code, or reaches it without infecting the state, or infects the state without
the difference propagating to an assertion. Each of those is a fact that
evidence goatest already collects can sometimes establish before any mutant is
built. Where it can, running the test would only confirm what is already
known.

## Decision

1. **No mutant is ever left unproved by policy.** There is no time budget,
   sampling rate, exclusion of slow targets, or prediction of which mutants to
   run, and none will be added. A run that is too slow is a run missing a
   proof, and the remedy is another proof.
2. **Every speed-up is a proof layer.** A layer is a rule that removes an
   execution because a lemma over evidence goatest already holds says the
   execution could not observe the mutant. The layers are ordered by the way a
   mutant survives: reach (the test ran no block containing the mutated
   position), infection (the test evaluated the condition but never took the
   branch the mutation narrows), and propagation, which is the next one to
   build. A layer that cannot establish its premise keeps the execution: the
   fallbacks are toward running more, never less.
3. **The lemma and the premise live on different sides.** The mutation engine
   states what it can prove about a mutant from the source alone — go-mutants'
   branch proof is the first such statement — and goatest checks the premise
   against its per-target evidence. Neither side trusts the other beyond the
   contract that names the claim, and a claim goatest does not understand is a
   claim it does not use.
4. **Every layer is visible.** A route records the granularity it was decided
   at, the fallback that widened it, and every target a proof discharged with
   the proof's name; a mutant resolved without an execution says so in its
   finding. The assurance contract states each layer's rule and the
   limitations document states what it cannot see.
5. **Every layer is audited independently before it ships, and stays
   auditable.** `proofaudit` reimplements each rule from a recording and the
   coverage profiles a run left behind, and holds it to every kill that run
   proved: a layer that would drop one recorded killer is unsound, and a layer
   ships only with zero violations on a real recording of this repository. The
   reimplementation is deliberate — the code under audit is not asked whether
   it agrees with itself.

## Consequences

- The cost of a surviving mutant remains the bound on a run, and it comes down
  only as proofs come in. That is the work; there is no setting that does it.
- A new proof is a change to two repositories: the engine gains a claim in its
  catalog, and goatest gains a rule that consumes it, a trace vocabulary that
  shows it, documentation that states it, and an audit layer that checks it. A
  proof without all four is not finished.
- Coverage still has a blind spot when a test reaches code only from a process
  that wrote no profile. ADR 0008 recovers it when a positive probe records the
  execution. ADR 0010 first asks an exact package-suite coverage control whether
  the fallback reached the position at all, then uses an infection-suite proof
  only where it can still answer; an unmeasured or unresolved execution remains
  fail-closed as the limitations document states.
- An audit needs a recording made with `--trace --keep-temp` and, for a layer
  that reads the catalog, the catalog of the same tree. Measurement runs are
  part of development here, and are run beside it rather than in its place.
- A reader who sees goatest go faster may ask which proof did it. The answer is
  always in the trace, never in a configuration file.
