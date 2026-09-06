# Assurance contract v1

## Meaning

An assured verdict is evidence about a named snapshot, resolved scope,
configured fault model, Go toolchain, platform, and declared execution inputs.
It is not a proof of program correctness and does not cover faults outside that
model.

`ASSURED` is reserved for a resolved `full` scope. A changeset run that remains
targeted receives `CHANGE_ASSURED`; an explicit package run receives
`SCOPE_ASSURED`. If changeset impact cannot be determined safely, the resolved
scope broadens to `full`, and the report records both requested and resolved
scope.

Replay is an operation, not a new project assurance. It returns `REPRODUCED`
when the selected finding remains observable or `RESOLVED` when it does not.

## Fault model

`standard-v1` requires:

- successful package discovery, build, vet, and native baseline execution;
- correct classification of top-level tests, fuzz seeds, examples, setup
  failures, skips, and custom test-binary arguments;
- every declared resource to become ready and shut down cleanly;
- race checks for packages selected by static concurrency and observed
  reachability (the estimate is always a report limitation);
- a complete `strong` go-mutants catalog and terminal disposition for every
  discovered mutant; and
- stable repair-candidate validation when a candidate is produced.

`deep-v1` uses the expanded operator set and exploration limits and runs the
race detector for every resolved package.

Benchmarks are not part of either correctness contract. A future performance
contract must be explicit rather than treating ordinary benchmarks as tests.

## Mutation routing

A mutant is run by the measured targets that reach it. Reach is decided by the
coverage blocks of the baseline profiles and the start position — line and
1-based byte column — the catalog reports for the mutation: a target reaches
the mutant when one of the blocks it executed contains that position.

The decision gives way to the whole file whenever the evidence cannot carry it.
A mutant with no reported position, and a position that lies in a gap between
the blocks the coverage toolchain cut, are both routed by every target that
executed the file. A checkpoint preserves those positive blocks and the blocks
its binary instrumented, and therefore preserves the exact decision even when
other baseline controls remain pending. Missing exact target coverage rejects
the checkpoint; missing partial instrumentation can only widen a continuation.

A position that instrumentation describes and no measured target executed
initially reaches nothing, exactly as for a mutant in a file no target covers.
The positive probe and package-suite control below then decide whether a hidden
target is recovered, the suite is already proved unchanged, or the suite must
run with the mutant. Such a mutant is `unreached` unless a target is recovered;
both an unreached and an ordinary surviving mutant are `survived` in the mutant
inventory, so the accounting equations below are unaffected by that route.

### Discharging a test a branch proof rules out

A reaching set decided by block is narrowed once more where the mutation itself
carries a proof. go-mutants publishes one for an edit that can only make the
condition of an `if` or a `for` less often true, and it names the span of the
body that condition gates, from its opening brace to its closing brace.

Write C for the original condition and C′ for the mutated one. C′ implies C,
and the whole condition is inert — no effects, no possible panic, guaranteed to
terminate. So a target during which no statement of the gated body ran
evaluated C to false every time it was evaluated, evaluated C′ to false there
too, took the same branch on every evaluation, and ran identically on the two
programs. It cannot have observed the mutation. Such a target is *discharged*:
removed from the reaching set without being executed, and named in the route's
`discharged` beside the proof that removed it. A discharge changes what a run
pays, never what it concludes.

The narrowing applies only where the evidence carries it. It is attempted on a
route decided by block with no fallback, and never on one decided by file. It
requires that the body was instrumented at all — some instrumented block must
begin inside the span — because otherwise no target's silence about the body
means anything. A fuzz target's deterministic seed execution is discharged by
the same proof as any other target. A target without exact block evidence has
no blocks to argue with.

A mutant every reaching target was discharged for is resolved without a single
execution, and reported as a `surviving-mutant`. It is not `unreached`: the
coverage blocks reach it, and running the package suite would only run the
tests the proof has already ruled out. That no test takes the branch the
mutation narrows is the finding — a real gap in the suite, stated for the cost
of reading a coverage profile.

### Discharging a test the probe pass shows cannot observe the mutation

A reaching set decided by block is narrowed a second time by what the probe pass
measured. Write coverage-reaching for the block decision after both discharge
proofs:

```text
coverage-reaching(m, t) = covered-block(m, t)
                        ∧ ¬branch-discharged(m, t)
                        ∧ ¬(probed(m) ∧ measured(t) ∧ m ∉ infected(t))
```

The probe tree is the program the user wrote, with no mutant ever active. For
each mutation the engine has a probe form of, that tree records — without
effects of its own — whether the value the original computed at the mutated site
ever differed from the constant the mutant would put there. So a target the pass
measured and that never saw that site differ ran the original program and the
mutated one through identical states: every value either program computed at the
site was the same value, and nothing downstream of the site could differ either.
It cannot have observed the mutation. Such a target is *discharged*, exactly as
a branch proof discharges one: removed from the reaching set without being
executed, and named in the route's `discharged` with reason `never-infected`.

That the recording is a proof is the engine's obligation, as the branch proof
is. A mutant does not evaluate the operand it replaced, so go-mutants attaches
a probe form only where leaving that operand unevaluated changes nothing
observable — every operand of the statement is effect-free — where the
recorded comparison is always reached — the replaced operand cannot panic —
and where equal values mean equal behaviour, which rules out a floating-point
or complex result. goatest states nothing about a site the engine did not
claim, and holds what the engine does claim to the recorded kills of every
dogfood run through the offline `proofaudit` infection layer.

The narrowing applies only where the measurement carries it, and everything else
is kept. A mutant the engine compiled no probe form for — `Mutant.Probed` is
false — is absent from every measurement there will ever be, so its absence from
one says nothing. A target the pass did not measure carries no facts at all: its
test failed, it timed out, the probe tree was unavailable, the execution
errored. A fuzz target's registered seed corpus is probed as an ordinary
deterministic target. An exact-input continuation may
restore only a complete probe phase bound to the same numeric-index mapping,
target inventory, and requested suite inventory. A partial or mismatched phase
is probed again in full. As for the branch proof, the narrowing
is attempted on a route decided by block with no fallback and never on one
decided by file: a file route is the answer routing falls back to when the
blocks cannot decide, and it is not narrowed further.
The all-or-nothing resume boundary and its catalog-index binding are
[ADR 0014](adr/0014-resume-complete-probe-phase.md).

Both proofs may answer for targets of the same route. They are applied in order
— branch first, then infection — so a target both would remove is recorded under
`branch-never-taken`, and the entries stay in run order whichever proof removed
each of them. A mutant every reaching target was discharged for is resolved
without a single execution and reported as a `surviving-mutant`, whichever proof
or pair of proofs answered.

### Recovering reach and proving the package-suite fallback

Probe absence narrows only a block route whose coverage already named the
target. Probe presence is different: a measured target that positively names a
probed mutant demonstrably executed its site, so it is added to the reaching
set even when coverage did not name it. The route records that widening as
`probe-reaching` and names the added targets separately. A target already
discharged by the branch proof is not re-added: that proof establishes equal
behaviour even when the condition itself computes a value the probe can see.

During baseline collection, goatest measures one coverage run of the exact
package suite used by the conservative fallback. It uses the same package and
test-binary arguments, and the union of every acquired resource environment;
a package suite may run targets with different resource declarations, so one
target's overlay is not an exact package command. For any mutation operator:

```text
passing(suite(m))
∧ instrumented(suite(m), position(m))
∧ ¬covered(suite(m), position(m))
    ⇒ execute(mutant(m), suite(m)) cannot observe m
```

The implication is used only for the exact start position inside a block the
coverage toolchain says it instrumented. An unknown position, a gap between
instrumented blocks, a failed or timed-out suite, and a missing profile are not
negative facts. They retain the package fallback. A positive target infection
is a concrete counterexample to coverage silence and recovers that target; a
positive suite infection likewise clears a conflicting negative coverage
decision. The route names the coverage control as `suite_coverage` and carries
`suite_reached` only when the measured suite covered the position.

Only packages that still contain an unresolved probed mutant receive the
second, semantics-preserving suite infection control. It is the same package,
arguments, and merged environment as the fallback, on a tree where no mutant
is active. For a probed mutant `m`:

```text
measured(probe-suite(m)) ∧ m ∉ infected(probe-suite(m))
    ⇒ execute(mutant(m), suite(m)) would survive
```

Every value the mutant could replace was equal to its replacement throughout
that execution, so activating it cannot change the suite. goatest records the
unreached finding and starts no mutant process. If the suite reached or infected
the mutant, goatest runs that whole suite: it is the compact execution that
retains `TestMain`, package setup, ordering, and cross-test interactions. If
neither proof is available, silence proves nothing and the suite remains.

For each distinct remaining package-suite request, goatest runs the semantic
original from the already-compiled probe tree once before any dependent mutant,
with no mutant active, and memoizes its result by package, arguments, and merged
environment.
A failed or timed-out original cannot distinguish a mutant failure from the
suite's own state, so all mutants sharing it become inconclusive without
repeating the command. A passing original supplies the closest duration
control. The deadline covers test execution, not a second workspace's cold
compilation. Mutant replay deliberately skips probe preparation and retains a
lazy pristine-workspace fallback.

These reach statements rest on one measured execution. A target or suite whose
behaviour differs between runs may enter a block, or make a site differ, in the
run that would kill the mutant and not in the one that was measured. Coverage
instrumentation and subprocess execution have their ordinary observational
limits as well. Positive infection overrides negative coverage, and every
unknown case falls through to the prepared semantic-original preflight and
mutant suite.
The full boundary is recorded in [limitations](limitations.md), and the
negative suite-coverage rule and its independent audit are specified by
[ADR 0010](adr/0010-whole-suite-reach-before-fallback.md).

These narrowings are proof layers in the sense of
[ADR 0004](adr/0004-proof-layers-not-budgets.md): an execution is removed only
where evidence the run already holds proves it could not observe the mutant,
never by a time budget, a sample, or an exclusion of slow targets, and a layer
that cannot establish its premise keeps the execution.

## Comparative mutation proof

A mutant is `killed` only after:

1. the exact original-code control for that request passes; and
2. one execution of the same request with the mutant active fails.

Each distinct control command — the package, arguments, and environment of the
killing request, which the original code does not vary by mutant — runs once
per mutation phase under the containment ceiling, and its outcome answers every
mutant that shares it. The snapshot is frozen for the whole phase and
re-verified afterwards. Repeating a mutant a finite number of times would not
prove determinism, so goatest does not pay for a second mutant execution or
treat repetition as stronger evidence.

A mutant that exhausts its budget is the one exception, and what answers it is
a fresh measurement rather than the expiration. goatest measures the exact
original once more, outside the memo. A second control that completes says the
request is still healthy on this machine, and buys exactly one more execution
under the containment ceiling — every derived budget is a claim about how long
the work takes, and this one has just been falsified. A second control that
fails or expires buys nothing, and the group is inconclusive. Expiration is
never the premise, the second control's completed duration is, and no
compatible group runs a mutant more than twice.

An original control failure or timeout at the ceiling is inconclusive and
prevents the mutant from starting. A mutation that does not compile is `compile-rejected`, never
“compile-equivalent”.

Every discovered mutant has exactly one report-v1 disposition:

```text
discovered = executed + compile-rejected + accepted + out-of-scope + unknown
executed   = killed + survived + inconclusive
selected   = executed + compile-rejected + accepted + unknown
```

The aggregate counts must exactly match the ID-level mutant inventory. Any
`unknown` disposition requires `ERROR`.

### Reusing a verdict an earlier run reached

The reasoning behind every rule in this section, and the alternatives it
rejects, is [ADR 0007](adr/0007-survived-evidence-is-universal.md).

A full run — the whole project, in a first round no repair has modified —
records what it established about every mutant it can state a checkable claim
for, and the next such run resolves those mutants from the records instead of
executing them. A kill is an existential claim — this exact compatible target
set kills this mutant — and a survival is the universal one — no test that
reaches this mutant kills it. The two are reused under conditions of the same
shape, over one witness set and over every target respectively.

#### A kill

The record names every target selected by the execution that killed the mutant,
and a later run resolves the mutant from it when all of the following hold:

1. the mutant has the same identity, which is content-addressed, so the
   mutated file is byte-for-byte what it was;
2. every recorded witness target is still routed to the mutant after every
   discharge above, and they still belong to one package and environment group;
3. every witness target has the same behaviour key and `whole_tree` boundary it
   had when the kill was recorded;
   and
4. this run's own baseline ran that target on the original tree and saw it
   pass.

The fourth is the control. The recording run watched the exact aggregate
original pass and the mutant fail; this run supplies fresh evidence that every
witness target's original still passes, and the report names the run that
supplied the rest. Additional reaching targets cannot falsify an existential
kill and do not invalidate the witness.

#### A survival

The record names every target the recording run executed against the mutant,
each with the behaviour key it had, and a later run resolves the mutant from it
when the mutant has the same identity and **every** target this run's coverage
routes to it, after every discharge above, is one of them: the same package,
name, and kind, the same behaviour key, and seen to pass by this run's own
baseline.

A reaching set smaller than the recorded one is still covered — a test that no
longer reaches the mutant cannot kill it — so the current set need only be a
subset. A target that entered it is a test nothing was ever run against, so the
universal claim is simply not about this run and the mutant executes. Two kinds
of target disqualify a survival in both directions: one without exact coverage
blocks and one whose corpus-bound behaviour key changed. A survivor whose whole reaching set the proofs discharged
is not recorded either: nothing ran to exhaust, and the proofs re-derive the
verdict on the next run without running anything.

#### A mutant no target reaches

Such a mutant is settled by the package suite, either by executing it with the
mutant or by its same-run probe proving that activation cannot change it. The
verdict is therefore a statement about that suite. Its key is the conjunction
of every target of the package — all kinds, fuzz targets included, because the
suite runs them as ordinary unit tests — each with its own behaviour key, and
of what the package-level run itself reads. A recorded verdict is reused when
the suite still has that key and nothing has come to reach the mutant; a
package this run could not measure whole, because a target has no exact blocks
or did not pass, names no key at all and neither
records nor reuses anything.

#### A timeout

A timeout is not a proof about the mutant. It says only that the verifier
stopped waiting, so it is always inconclusive and is never stored or reused as
mutation evidence.

`[execution].timeout` is the user-controlled containment ceiling. It caps every
derived original-control and mutation budget and remains the operating-system
limit for arbitrary mutated code. It is not by itself a routine mutant budget.

A routine mutant budget is the saturating sum of distinct positive clean
durations from this run that describe the request. A target request uses its
passing baseline and separately measured probe when both exist. A combined
baseline-and-probe execution contributes once. Applicable target and
package-suite observations all contribute to an aggregate rather than choosing
the shorter representation. Before a mutant request, goatest runs the exact
semantic original once under that already-derived deadline and memoizes it by
package, arguments, environment, and deadline. A passing positive duration
joins the other distinct observations; a failure or expiration makes that
group inconclusive without starting its mutant.

With no positive prior observation, a mutant request does not start its control
or mutant subprocess and the result is `mutation-control-unavailable`.

For a mutation reached by several targets, goatest partitions them by exact
package and environment. Every compatible group runs once under one exact
`-test.run` selector. No measured-cost, target-count, duration, or argument-size
heuristic changes this partition. Targets have canonical identity order, so
equal sets share an exact-original control across mutants. Groups use summed
measured duration and execution identity only as their deterministic schedule.
This is not sampling: every reaching target
is either discharged by a proof or belongs to a selector that actually ran
with the mutant active. A completed passing aggregate proves all named targets
passed. A completed failing aggregate is a terminal kill proof for its exact
target set. Neither result is split or retried for attribution. Timeout,
unavailable-control, and inconclusive outcomes remain unknown for that group
while later groups continue; any later completed kill decides the mutant. If
none kills, all unknown groups are reported in deterministic plan order and
survival is reported only when every group passed. An execution, API, or
protocol error or context cancellation aborts the run. A selector too large
for the host's process API fails explicitly instead of being divided at a
guessed argv limit. See
[ADR 0012](adr/0012-aggregate-proof-before-timeout.md).

No contract-specific timeout floor or cap participates in this formula. No
finite verifier can distinguish every slow terminating computation from
nontermination, so a machine that slows after the controls may still produce
an inconclusive timeout. One initial mutant expiration ends that path: goatest
does not recalibrate, run a post-timeout original, or retry the mutant. An
an expiration cannot erase a completed kill from another compatible group.
The complete argument is
[ADR 0018](adr/0018-confirm-comparative-watchdogs.md).

#### The behaviour key

The behaviour key is an allowlist over what the run already digested for its
own snapshot identity: every Go file of the packages the target's test binary
links, the `testdata` and embedded files beside those packages, the module
manifests, the external module digests, the toolchain, the platform, the
selected base environment overlaid by the target's resource environment, the
contract, the test arguments, the build tags, both timeouts, the goatest
version, the exact running goatest executable digest, the
go-mutants version embedded in that executable's Go build information, and a
fuzz target's corpus. When the linker omits that dependency entry or a local
replacement has no module version, the go-mutants identity is bound to the
exact running executable's SHA-256 instead. If neither identity can be read,
goatest stops before reading or writing reusable evidence; it never substitutes
a source-tree or release-time version guess. A
dependency's own `_test.go` files are outside the key, because they are never
compiled into the binary; the target's own package's test files are always in
it. Diagnostics — tracing and kept temporaries — are outside every key. The job
count is also outside: it changes only which independent processes overlap,
while their evidence is committed in a fixed order; an exclusive declared
resource forces one job, and any timeout caused by contention is inconclusive
rather than reusable positive proof.

Environment overlays use the process platform's key case semantics and
last-wins values, then sort the effective entries. A package-suite key uses the
complete suite resource environment and the conjunction of its target keys.

A selected target or package suite receives a Go test action log when its
package can reach a repository read and static inspection places every such
read inside the observable interval. The log records the `open`, `stat`, and
`chdir` operations of both the baseline and every mutant execution used to
establish a record, including operations reached through dependencies or
helpers that static source inspection cannot see. A package that can reach no
repository read at all is already narrow and is not observed. A package whose
reads static inspection places outside that interval is widened to the whole
tree without an observation, because an unobserved execution can never narrow a
key. A named ordinary input already present in the closure key leaves it
narrow. A
repository directory, a missing path, or a file outside that input set widens
the target to every file of the snapshot. A batch's observation applies to
every target it selected. A killing execution's observation is recorded
directly, so a mutant that opens a repository-reading branch the baseline did
not take still gets a whole-tree key.

Every stored target or suite key carries an explicit `whole_tree` value. A
current baseline that requires the whole tree never accepts a narrow record.
Missing, malformed, truncated, or otherwise ambiguous completed action logs
widen rather than narrow the key, as does a log goatest could not create or
read back. A test binary that reports it could not write the log it was given
is an infrastructure error, and the proof execution is not repeated without
observation. Known path reads reachable from package initialization or
`TestMain`, generic `io/fs` uses whose backing store cannot be observed,
`os.Readlink`, raw `syscall` and `golang.org/x/sys` calls, `cgo`, a subprocess
started through `os/exec`, `plugin.Open`, and source parse/list failures remain
statically whole-tree.
Static analysis can only widen this boundary; absence from its API list never
disables runtime observation. Nothing is excluded from testing or reuse by
name, and no configuration or annotation is required.

Kills established by deterministic fuzz seed targets are persisted like other
target kills, with the corpus in the behaviour key. An aggregate kill stores
the complete selected target set. It is reusable when every stored target still
reaches, passed its current clean control, has the same behaviour key and
`whole_tree` boundary, and the complete witness belongs to one current package
and environment group. Additional reaching targets do not invalidate this
existential kill proof. A changed or incomplete witness is ignored and the
mutant executes; a record about a mutant no longer in the catalogue is dropped
when the store is written.

Reuse is confined to the run a record can be a claim about: a first round,
because a later round verifies a tree an earlier round repaired; the whole
project rather than a changeset or a package scope, which narrow the claim; no
configured resources, which carry runtime state a digest cannot see; and no
replay. `goatest replay` therefore always executes, which is what makes it a
reproduction. A store that cannot be read is discarded with a progress note and
the round executes everything; a store that cannot be written is a note and
nothing more.

Nothing expires a record and nothing decides one is old. A stale record is
removed by being contradicted: every mutant a run executes writes a fresh
record, and this run's record replaces the one it was read from, so a kill the
tests no longer make and a survival they now contradict each replace the other.

A reused verdict is one of the executed dispositions, not something beside
them: `reused_killed + reused_survived <= executed`, each reused disposition
carries the `provenance` of the run that observed it, and its route in the
trace records the reuse with no execution beside it. A reused verdict raises
its finding again through the acceptances of the run reading it, never through
the recording run's, so an acceptance that has since expired resurrects the
finding and one that still holds silences it — which is the one disposition
outside the executed three a reuse reaches. A reused mutant that is then
checkpointed carries its provenance through the checkpoint, so a run resuming
it reports the reuse rather than claiming it observed the verdict.

## Acceptances

An acceptance is human authorization, not a mutation result. It requires a
finding ID, non-empty reason, future RFC3339 expiry, and may carry owner and
ticket. Report v1 includes that metadata, and every mutation marked `accepted`
must reference a matching record. Expired acceptances are ignored and cannot be
persisted as evidence for a run that started after expiry.

## DEFECT, INSUFFICIENT, and ERROR

`DEFECT` means user code violated a baseline, race, build, vet, or test
contract. `INSUFFICIENT` means execution completed but a survivor, flaky or
inconclusive outcome, excluded boundary, or other
evidence gap remains. `ERROR` covers incomplete accounting and toolchain,
provider, filesystem, protocol, or workspace failures.

A limitation is always structured with a stable code. Excludes, estimates,
unavailable metadata, and skipped later phases must never be hidden behind an
assured-looking percentage.
