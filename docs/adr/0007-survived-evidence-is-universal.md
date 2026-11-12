# 0007 — Survived evidence is a universal proposition over the reaching set

## Status

Accepted, 2026-09-04. Implemented by `internal/evidence` (the record shapes,
the target and suite behaviour keys, and the store's shape rule),
`internal/assure` (`mutation_evidence.go` for the conditions and the writers,
`mutation.go` for the routing that consults them, `resume.go` for the
provenance a checkpoint carries), `internal/golang/readers.go` (the
repository-reader rule), and the reuse classes in `internal/devtools`.

## Context

ADR 0004 committed the project to speed by proof: no budget, no sampling, no
exclusion, and every speed-up a rule that removes an execution because
something already known says the execution could not observe the mutant. Reuse
across runs is such a rule. What an earlier run observed about a mutant is
evidence, and re-observing it is work that establishes nothing new — provided
the claim the earlier run made is still a claim about this run.

[ADR 0002](0002-trace-is-not-evidence.md) and the assurance contract fixed the
first half of that: a kill is reused when every target in the recorded witness
set still reaches the mutant in one compatible group, still has the same
behaviour key, and passed this run's own baseline.
That covers the existential half of a mutation phase. It does not cover the
expensive half. On this repository, survivors are most of the mutation phase:
every reaching test of a surviving mutant runs to completion, because nothing
short of running all of them establishes that none kills it, and the result is
re-derived from scratch on every run although nothing that could change it
changed.

The difficulty is that a survival is a different kind of claim. "This exact
target set killed the mutant" is existential: one witness set makes it true,
and checking that the complete witness is unchanged is enough. "No test that
reaches this mutant kills it" is universal: it is a claim about a set, and a
set that gained a member is a
different set. A condition modelled on the kill rule — say, "some recorded
target still reaches it unchanged" — would be unsound in exactly the case that
matters, a newly written test that kills a mutant an earlier run watched
survive.

## Decision

1. **A survived record is a universal proposition over the targets that were
   executed.** The record names every target the recording run ran against the
   mutant, each with the behaviour key it had. A later run reuses it only when
   **every** target its own coverage routes to the mutant, after every
   discharge, appears in that set with an equal key and passed this run's
   baseline. One target failing any condition ends the reuse and the mutant
   executes.

2. **A subset is sound; a superset is not.** A target that no longer reaches
   the mutant cannot kill it, so the current reaching set being a proper subset
   of the recorded one leaves the claim intact. A target that entered the set —
   a new test, a renamed one, one whose kind changed — was never run against
   the mutant, so nothing is known about it and the claim is not about this run
   at all. The asymmetry is the whole rule: reuse is refused by growth, never
   by shrinkage.

3. **Fuzz seed targets qualify only under their corpus-bound key.** Verification
   executes the registered seed corpus deterministically. Its digest is part of
   the target key, so any corpus change invalidates a recorded kill or survival.
   A target without exact coverage blocks belongs to a wider set than any run
   measured and does not qualify.

4. **A mutant no target reaches is a claim about the package suite.** The claim
   is established by exact negative suite coverage, by a semantics-preserving
   whole-suite probe proving activation cannot change the execution, or by
   executing that suite with the mutant active after a passing original
   preflight. The suite runs every target of the package, fuzz targets included,
   as one command, so its behaviour is the conjunction of its targets'
   behaviours and of what the package-level run itself reads. The suite key is
   built from exactly those two, in a hash domain of its own so that it can
   never be confused with the behaviour key of one target. A package this run
   could not measure whole names no suite key, and neither records nor reuses
   anything.

5. **A timeout is not evidence.** A timeout establishes nothing about the
   mutant or the reaching set. It is reported as an inconclusive finding for
   the current run and is never written to the reusable store. A later run must
   obtain completed executions to establish a kill, survival, or unreached
   verdict; otherwise it remains inconclusive again.

6. **An execution that reads the repository keys the whole tree, rather than
   excluding its package.** Go's test action log observes every selected
   baseline, package suite, and mutant execution that establishes a record,
   except those whose package static inspection has already placed outside the
   observable interval or behind an API the log cannot see; such a package is
   widened without an observation. Access to a repository directory, a
   missing repository path, or a file outside the ordinary closure widens that
   target or suite to the whole snapshot; accesses to temporary directories
   and already-keyed files do not. A killing mutant execution contributes its
   observation directly, and a batch applies its observation to every selected
   target. An absent, malformed, or ambiguous completed log always widens; a
   failure to produce the log is an infrastructure error and is not retried
   without observation. Thus a
   reused narrow record exists only when the baseline and killing mutant
   execution were observed inside its stated input set; a mutant-only reader
   branch cannot escape the key. Known pre-`M.Run` cases, generic-FS uses, raw
   syscalls, cgo, subprocesses, and loaded plugins remain statically
   whole-tree. ADR 0004 still forbids exclusions: the rule changes
   what a verdict may be reused across, never which mutants are tested.

7. **Contradiction removes a record; nothing else does.** Every mutant a run
   executes writes a fresh record and this run's record replaces the one it was
   read from, so a kill the tests no longer make and a survival they now
   contradict each replace the other. There is no expiry, no age, and no
   heuristic about staleness — the only thing that unseats a claim is an
   observation that disagrees with it, and a record about a mutant the
   catalogue no longer names is dropped when the store is written back.

8. **Every reuse is visible and subordinate to this run.** The route in the
   trace says `reused: true` with the reuse as its whole plan and no execution
   beside it; the disposition carries the flag and the provenance of the run
   that observed the verdict; `reused_killed` and `reused_survived` count the
   reuse inside `killed` and `survived`. The finding is raised again through
   *this* run's acceptances, never the recording run's, so an acceptance that
   has expired resurrects the finding. A reused mutant that is checkpointed
   carries its provenance through the checkpoint, so a resumed run reports the
   reuse rather than claiming the verdict as its own.

## Consequences

The expensive half of the mutation phase becomes reusable, and the store holds
three outcome shapes. The conditions are strictly narrower than
those for a kill, so a repository whose tests change often will reuse fewer
survivors than kills — which is the correct behaviour, not a shortfall: a
changed test binary is a test binary nothing was ever run against.

Every observed execution pays for one transient action log, while the
whole-tree key cost remains limited to targets that actually cross the key
boundary or are statically unobservable. Whole-tree target and suite keys are
generated lazily and cached. Tests that use repository APIs exclusively on
temporary directories retain narrow evidence, while repository-wide gates and
every uncertain observation retain the conservative behaviour. The refinement
adds no user configuration, stored paths, or weaker fallback.

`proofaudit` gains a class rather than a rule. A reused route is not a measured
kill it can hold to a layer, and counting it as one would be auditing evidence
this run did not produce; it is counted separately so that the audited share of
a run is read against the part of the run that ran. What keeps reuse honest is
the store's own strictness — a record that contradicts its outcome is neither
written nor loaded — and the end-to-end tests that run a real toolchain twice
and compare verdicts.

## Alternatives rejected

- **Reusing a survival when some recorded target still reaches the mutant.**
  Unsound: a test added since the recording can kill a mutant an earlier run
  watched survive, and this rule would never notice.
- **Excluding repository-reading packages from reuse, or from mutation.** ADR
  0004 forbids exclusions and budgets, and an exclusion answers the wrong
  question: the problem is what a key claims to know, not which packages are
  worth testing.
- **Expiring records by age or run count.** Age is not evidence. A record is
  either still a claim about this run, in which case time has not changed it,
  or it is not, in which case a condition already refuses it.
- **Persisting a timeout.** A timeout resolves nothing. Storing it would make a
  non-answer durable without providing a premise for a later proof.
