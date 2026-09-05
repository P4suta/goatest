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
executed the file, which is what routing did before blocks were read. A target
restored from a checkpoint carries no blocks and keeps reaching its whole file.

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
means anything. A fuzz target is never discharged: it explores inputs beyond
the corpus its coverage was measured on, so its blocks do not bound what it
will execute. Neither is a target restored from a checkpoint, which carries no
blocks to argue with.

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
errored, it was restored from a checkpoint, or it is a fuzz target, which the
pass never probes because fuzzing explores past the corpus a probe would
measure. As for the branch proof, the narrowing is attempted on a route decided
by block with no fallback and never on one decided by file: a file route is the
answer routing falls back to when the blocks cannot decide, and it is not
narrowed further.

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

Before mutation preparation, goatest measures one coverage run of the exact
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

Immediately before any remaining mutant package-suite command, goatest runs
the exact uninstrumented original command and memoizes its result by package,
arguments, and merged environment. A failed or timed-out original cannot
distinguish a mutant failure from the suite's own state, so all mutants sharing
it become inconclusive without repeating the command. A passing original
supplies the closest duration control and also serves paired kill confirmation.

These reach statements rest on one measured execution. A target or suite whose
behaviour differs between runs may enter a block, or make a site differ, in the
run that would kill the mutant and not in the one that was measured. Coverage
instrumentation and subprocess execution have their ordinary observational
limits as well. Positive infection overrides negative coverage, and every
unknown case falls through to the exact original preflight and mutant suite.
The full boundary is recorded in [limitations](limitations.md), and the
negative suite-coverage rule and its independent audit are specified by
[ADR 0010](adr/0010-whole-suite-reach-before-fallback.md).

These narrowings are proof layers in the sense of
[ADR 0004](adr/0004-proof-layers-not-budgets.md): an execution is removed only
where evidence the run already holds proves it could not observe the mutant,
never by a time budget, a sample, or an exclusion of slow targets, and a layer
that cannot establish its premise keeps the execution.

## Mutation confirmation

A mutant is `killed` only after:

1. an initial mutant execution fails;
2. the original-code control for that request passes; and
3. a second execution of the same mutant/request also fails.

Each distinct control command — the package, arguments, and environment of the
killing request, which the original code does not vary by mutant — runs once
per mutation phase and its outcome answers every kill that shares it. The
snapshot is frozen for the whole phase and re-verified afterwards, so a
remembered control is the same evidence as a repeated one without the repeated
cost.

An original control failure is `flaky-mutation-control`; a non-reproducing kill
is `flaky-mutation-kill`. Both are inconclusive evidence and prevent an assured
verdict. A mutation that does not compile is `compile-rejected`, never
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
executing them. A kill is an existential claim — this named target kills this
mutant — and a survival is the universal one — no test that reaches this mutant
kills it. The two are reused under conditions of the same shape, over one
target and over every target respectively.

#### A kill

The record names the target that confirmed the kill, and a later run resolves
the mutant from it when all of the following hold:

1. the mutant has the same identity, which is content-addressed, so the
   mutated file is byte-for-byte what it was;
2. the recorded killer is a target this run's own coverage still routes to the
   mutant, after every discharge above;
3. that target has the same behaviour key it had when the kill was recorded;
   and
4. this run's own baseline ran that target on the original tree and saw it
   pass.

The fourth is the control. The recording run watched the mutant fail, the
original pass, and the mutant fail again; this run supplies fresh evidence that
the original still passes, and the report names the run that supplied the
rest.

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
of target disqualify a survival in both directions: a fuzz target, because
exploring one budget without finding an input says nothing about the next, and
a target restored from a checkpoint, which carries no coverage blocks and is
therefore routed for the whole file, so the set it belongs to is wider than the
one any run measured. A survivor whose whole reaching set the proofs discharged
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
package this run could not measure whole, because a target of it was restored
from a checkpoint or did not pass, names no key at all and neither records nor
reuses anything.

#### A timeout

A timeout is not a proof about the mutant: it says the run could not settle it.
Reusing one therefore keeps a finding and never removes one, which is the only
direction in which a question nobody answered may be carried forward.

An ordinary mutation command is bounded relative to controls from the same
run, not by one fixed routine timeout. A target mutation uses its passing
baseline and measured probe durations. A package-suite mutation uses its
passing whole-suite coverage control and, when available, the suite probe; the
exact uninstrumented preflight immediately before the mutant is another sample.
Let `slow` and `fast` be the slowest and fastest positive control samples
available for that request:

```text
margin = max(1 second, slow, 8 × (slow - fast))
comparative deadline = slow + margin
```

The extra copy of `slow` admits ordinary slowdown, the spread term admits the
load variation the controls actually observed, and one second covers process
scheduling for tiny tests. The probe that obtains a first control uses the same
rule from the passing baseline; the first whole-suite controls use the sum of
that package's passing target durations. If no positive control exists, goatest
fails closed to the legacy five-times-baseline-plus-five-seconds calibration
with a 30-second floor. On an ordinary package-suite route that legacy wait can
occur at most once, in the memoized original preflight; a failure or expiration
settles every dependent mutant as inconclusive without running them. Fuzz
campaigns retain that legacy bound because the duration of their seed corpus
does not predict a fixed 10,000- or 100,000-input campaign.

The contract caps every derived deadline at 30 minutes for `standard-v1` and
five hours for `deep-v1`; `[execution].timeout` is a further hard cancellation
ceiling, not the normal amount of time each mutant waits. No finite verifier
can distinguish every slow computation from nontermination, so removing an
absolute cancellation boundary would permit a mutant to hang the run forever.
The contract instead removes fixed time from ordinary classification: a
deadline is comparative evidence for when to stop waiting, and its expiration
is always inconclusive. It never manufactures a kill or a survival.

Its condition is existential, not universal, because a timeout is one
observation about one target: this target did not finish in the time it was
given. The targets that ran before it neither caused that nor say anything
about whether it finishes now, and a target that has since joined the reaching
set says nothing about it either. A recorded timeout is reused when the target
time ran out under still reaches the mutant, has the same behaviour key, passed
this run's baseline, and is neither a fuzz target nor one restored from a
checkpoint. The record names that target as the **last** of its executed
targets — a timed-out record is stored in execution order for exactly that
reason, where a survived record's targets are stored sorted, because there the
set is the claim.

A timeout under the package suite of a mutant no target reached names the suite
instead, and is reused under the suite key like any other statement about a
suite. `goatest replay <finding-id>` bypasses evidence entirely, which is how a
timeout is deliberately re-run.

#### The behaviour key

The behaviour key is an allowlist over what the run already digested for its
own snapshot identity: every Go file of the packages the target's test binary
links, the `testdata` and embedded files beside those packages, the module
manifests, the external module digests, the toolchain, the platform, the
selected environment, the contract, the test arguments, the build tags, both
timeouts, the goatest and go-mutants versions, and a fuzz target's corpus. A
dependency's own `_test.go` files are outside the key, because they are never
compiled into the binary; the target's own package's test files are always in
it. Diagnostics — tracing and kept temporaries — are outside every key. The job
count is also outside: it changes only which independent processes overlap,
while their evidence is committed in a fixed order; an exclusive declared
resource forces one job, and any timeout caused by contention is inconclusive
rather than reusable positive proof.

Sources that use `os.ReadDir`, `os.DirFS`, `os.OpenRoot`, `filepath.Walk`,
`filepath.WalkDir`, `filepath.Glob`, `fs.WalkDir`, `fs.ReadDir`, `fs.Glob`, or
`fs.Sub` select a package for repository-read observation. For those packages,
Go's test action log records the `open`, `stat`, and `chdir` operations of both
the baseline target and every mutant execution used to establish a record. A
named ordinary input already present in the closure key leaves it narrow. A
repository directory, a missing path, or a file outside that input set widens
the target to every file of the snapshot. A batch's observation applies to
every target it selected, and paired kill confirmations are combined, so a
mutant that opens a repository-reading branch the baseline did not take still
gets a whole-tree key.

The stored target or suite key carries `whole_tree: true` when widened; its
absence is the narrow form and is also how old records are read. An old
package-wide reader record therefore misses its new narrow key once and is
re-established in the precise form. A current baseline that requires the whole
tree never accepts an old narrow record. Missing, malformed, truncated, or
otherwise ambiguous action logs widen rather than fail the run. Direct uses in
package initialization or `TestMain`, generic `io/fs` uses whose backing store
cannot be observed, and source parse/list failures remain statically
whole-tree. Nothing is excluded from testing or reuse by name, and no
configuration or annotation is required.

Two kinds of kill are neither recorded nor believed. A kill fuzzing found is a
claim about one budget, not the next. A kill by a batch or a package suite
proves that one of several targets killed the mutant without naming which, so
there is nothing a later run could check the key of. A record whose killer no
longer reaches, whose key changed, or whose control did not pass is ignored and
the mutant executes; a record about a mutant no longer in the catalogue is
dropped when the store is written.

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
inconclusive outcome, unpersisted fuzz kill, excluded boundary, or other
evidence gap remains. `ERROR` covers incomplete accounting and toolchain,
provider, filesystem, protocol, or workspace failures.

A limitation is always structured with a stable code. Excludes, estimates,
unavailable metadata, and skipped later phases must never be hidden behind an
assured-looking percentage.
