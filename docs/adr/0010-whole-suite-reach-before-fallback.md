# 0010 — Whole-suite reach before fallback execution

## Status

Accepted, 2026-09-05. Implemented by the package-suite coverage controls,
filtered suite probes, exact original-suite preflight, route trace fields, and
the independent `proofaudit` suite-reach layer.

## Context

The original reach rule deliberately widened to a package suite whenever no
isolated target reached a mutant. That was safe, but it conflated two very
different cases: code the suite reaches through an execution that per-target
coverage cannot see, and code no execution of the suite reaches at all.

On goatest's first full recording, thirteen packages fell into that fallback.
They contained 62 percent of all mutants, including 2,302 in `internal/assure`,
1,053 in `internal/app`, and 675 in `internal/buildcache`. `goatest` itself is
an especially bad case for a source-only approximation: production code calls
operations such as `os.ReadDir`, so treating every test in such a package as a
possible repository reader makes the package-wide execution boundary spread
far beyond the tests that actually read the repository.

The fallback also made a timeout expensive in the wrong dimension. A single
uncertain package could pay one whole-suite deadline for every mutant no target
reached. Even a better deadline still repeats the same unanswered question.
The useful question is earlier and shared: did the exact suite execute this
source position on the original program?

## Decision

1. **Measure the exact fallback once with coverage.** After the isolated
   baseline targets pass, goatest runs one whole-package coverage control for
   every package that owns a selected target. It uses the package's compiled
   coverage binary, the run's test-binary arguments, and the union of all
   acquired resource environments. Whole-package execution may run targets
   with different resource requirements, so choosing one target's environment
   would not describe the command the fallback later runs.
2. **Use only an exact negative statement.** For a mutant `m` and that measured
   suite `s`, routing may discharge the package fallback only when cmd/cover
   instrumented the exact start position of `m` and no covered block of `s`
   contains it:

   ```text
   passing(s)
   ∧ instrumented(s, position(m))
   ∧ ¬covered(s, position(m))
       ⇒ s did not execute position(m)
       ⇒ activating m cannot change that execution
   ```

   This proof is independent of the mutation operator and therefore also
   applies where go-mutants has no infection probe form. A missing position, a
   position outside all instrumented blocks, a missing profile, a failed or
   timed-out suite, or any decoding ambiguity proves nothing and retains the
   fallback.
3. **A positive observation defeats silence.** A positive target infection
   recovers that target even when coverage was silent. A suite infection
   contradicting negative suite coverage likewise clears the negative
   conclusion and keeps the package fallback. Positive evidence is a concrete
   counterexample; it is never discarded to preserve a cheaper route.
4. **Probe only unresolved package suites.** Target probes still run because
   they can repair coverage-blind routing and discharge individual executions.
   A package-suite infection probe runs only when some probed mutant still
   needs the fallback after ordinary routing and suite coverage. A negative
   suite-coverage proof has already answered every operator at that position;
   repeating the weaker operator-specific measurement would add no evidence.
5. **Preflight the semantic original before remaining fallbacks.** Before any
   mutant is run by a package suite, goatest runs the already-compiled probe
   tree with no mutant active and exactly the same package, arguments, merged
   environment, and execution shape. The result is memoized by that command
   identity. If the original fails or reaches its safety ceiling, every mutant
   waiting on that command is inconclusive without executing another copy. If
   it passes, its duration is a same-run control sample for the mutant's
   comparative deadline and the same result can serve later paired kill
   confirmations. Replay skips the probe tree and uses a pristine fallback.
6. **Record and audit the proof independently.** A route names its passing
   control as `suite_coverage`; `suite_reached` is present only when a covered
   block contained the exact position. `proofaudit` reconstructs package-suite
   profiles from recorded baseline commands, independently reimplements block
   containment, deduplicates paired kill confirmations, and requires that every
   attributable package-suite kill be kept. Missing, conflicting, or
   unattributable evidence is reported as unverifiable, never as a clean pass.

## Soundness boundary

The implication is about one measured execution. It assumes the ordinary Go
coverage contract: instrumentation identifies execution of the source block,
and changing an expression cannot affect control flow before that expression is
reached. It does not assume that an uncovered source file is dead, nor that a
gap between instrumented blocks means absence; both retain the fallback.

The proof shares the established reach layer's observational limits. A
nondeterministic suite may reach the position on another run. Code executed in
a subprocess or through a mechanism that does not contribute to the profile
may be invisible. Coverage instrumentation itself may perturb behaviour. These
limits are explicit, positive probe observations override negative silence,
and every unresolved case reaches the exact uninstrumented preflight and mutant
suite rather than being guessed away.

## Consequences

- Thousands of mutants can be classified from one passing suite profile rather
  than each paying a package process or a deadline.
- The remaining whole-suite mutants have a passing original command immediately
  beside them, so routine execution is calibrated from the actual fallback and
  an unhealthy package costs one control rather than one wait per mutant.
- Cold runs gain a bounded package-level control phase. The controls run in
  deterministic import-path slots with the configured job limit, so their wall
  time overlaps without changing report or checkpoint ordering.
- This does not claim that an arbitrary program terminates. It removes repeated
  work only where a finite observation proves that a particular execution did
  not reach the mutation; every other case remains fail-closed.
