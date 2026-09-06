# Interrupted assurance checkpoint v1

`assurance-checkpoint-v1` is strict scheduling state for continuing an
interrupted verification. It is never assurance evidence, never a partial
`assurance-report-v1`, and never updates `latest-any` or `latest-full`.

## Location and ownership

One exact input identity owns one checkpoint:

```text
.goatest/cache/v1/<input-digest>/checkpoint-v1.json
.goatest/cache/v1/<input-digest>/checkpoint-journal-v1.jsonl
```

The JSON document is the strict complete base at phase boundaries. Between
those boundaries, the JSONL journal durably appends each completely classified
baseline target or mutant without rewriting the growing base. Both sit beside,
but are independent from, the completed cached `report.json`. Base writes use a
synced temporary file and atomic rename; journal appends are synced individually.
Cache TTL and capacity GC remove the whole digest directory, so neither form of
checkpoint state can outlive its cache policy. The crash protocol and its
rationale are [ADR 0011](adr/0011-append-only-checkpoint-journal.md).

Verification holds an OS advisory lock on `.goatest/cache/.lock` from before
cache/checkpoint access through durable report persistence and checkpoint
deletion. Cache status and GC take the same lock. Another process emits one
`cache-wait` note and polls until the owner exits; cancellation interrupts that
wait before verification or GC starts.

## Exact-input reuse

Every attempt rescans source and corpus content, configuration, dependency
content and sums, toolchain and platform, selected environment, tool versions,
the exact running goatest executable, contract, package/scope options, build
tags, test-binary arguments, timeouts, and mutation settings to compute the
input digest. There is no CLI resume flag: only a checkpoint under the newly
computed, identical digest is considered.

Configured resource providers disable checkpoint reuse because their runtime
state is not captured. Only repair round zero may reuse a checkpoint. A corpus
promotion or generated source change deletes and disables the old checkpoint
before the next round.

A saved baseline target carries the exact positive coverage blocks it measured,
so interruption does not widen block routing into a whole-file approximation.
Its resource requirements exist only as the canonical `capabilities` array;
the checkpoint schema accepts no singular alias.
The append-only journal writes each target's blocks once; it does not rewrite
earlier targets. On this repository's 1,115-target dogfood baseline, 129,595
positive blocks add 25.8 MB to an 8.7 MB checkpoint and deterministic encoding
takes 0.21 seconds. With the same aggregate planner, preserving them removes
4,145 planned executions (82,353 to 78,208) from the recorded interrupted route
set and keeps exact-block survivor evidence eligible for reuse. The durable
size is therefore bounded scheduling state with a measured payoff rather than
repeated profiles.

Instrumentation is package-binary evidence rather than target evidence: every
isolated target in one package runs the same already-compiled coverage binary.
An unfinished baseline therefore stores that identical set on at most one
deterministically selected target anchor per package and omits it from the rest.
A pending or unmeasured anchor means unknown and can only widen resumed work. A
433-target interrupted self-run used five anchors and a 10.4 MB journal;
repeating the package sets on every target would have added about 178.7 MB. At
baseline completion even those anchors are removed and the deduplicated global
set lives only in `routing`.

`coverage` is required whenever a saved unit carries target evidence.
`instrumented` is optional only on an incomplete baseline, where at most one
completed target per package owns the recovery anchor. Missing instrumentation
cannot establish that an uncovered position was measurable. A present empty
coverage object is an exact empty measurement. [ADR 0013](adr/0013-preserve-block-routing-across-resume.md)
records the decision.

At baseline completion the checkpoint also stores the deduplicated global
instrumented block set and each successful package-suite coverage control. A
later exact-input attempt can reconstruct the completed baseline without
recompiling coverage binaries or rerunning package suites. While the baseline
is unfinished, every measured or conservatively unmeasured package-suite control
is journaled independently and is not repeated by a continuation. Missing
package-level state is never inferred from target blocks.

A complete target and package-suite probe pass is stored once at its phase
boundary, including measured/unmeasured classification, infection indices,
control durations, and suite whole-tree selection. The compact indices are
bound to a SHA-256 fingerprint of the exact index-to-mutant and probe-capability
mapping in the prepared catalog. Restore also requires the exact target and
requested-suite inventories. A mismatch discards both the probe and every
mutant result routed with it.

No partial probe is ever stored. Cancellation before the phase boundary or any
invalid mapping repeats the complete pass; unmeasured entries remain
conservative and never become negative facts.
The fresh attempt emits `probe-exec` records. A restored attempt emits the
`resume-probe` progress note but no pretend execution record, so trace command
counts remain physical counts. The decision and its proof are
[ADR 0014](adr/0014-resume-complete-probe-phase.md).

Repository-read observation is different because a resumed mutation verdict
must retain the input boundary established by its baseline. A saved target
therefore carries whether it selected the whole-tree key.

## Save boundaries

The complete base checkpoint is atomically replaced at each structural phase
boundary:

- build and vet have both completed;
- the baseline phase has completed;
- the complete selected race phase has finished;
- mutation catalog validation or invalidation has completed; and
- the complete semantic-original probe phase has finished; and
- the mutation phase has completed.

Inside the baseline and mutation phases, each terminal unit is instead one
append-only journal record. A baseline target is appended after its passed,
skipped, failed, or not-run classification is complete; a package-suite control
is appended after it is measured or conservatively classified unmeasured. A
mutant is appended the moment nothing more can be learned about it. Each record
names the input digest and the SHA-256 digest of the base document it extends,
contains exactly one unit, carries its own checksum, ends in a newline commit
marker, and is synced before publication returns. This retains per-unit
durability without rewriting all earlier units after every process.

Baseline workers may finish out of order. The coordinator publishes every
complete unit immediately, even when an earlier target is still running or will
fail, and treats the growing target and suite collections as identity-keyed
sets. A worker never writes checkpoint state itself. Replay and phase-boundary
publication sort those sets, while the report is reconstructed in discovery
order. The completed bytes and selected infrastructure error are consequently
independent of scheduler order, and interruption can expose only complete
journal records, never a partially decoded coverage profile or repository
observation. See [ADR 0016](adr/0016-publish-every-baseline-control.md).

A mutant is terminal the moment nothing more can be learned about it, not when
the phase around it ends. A mutation every reaching test passed is saved as a
surviving mutant right there, and so is one whose every reaching test a branch
proof discharged, which is terminal without having been executed at all. A fuzz
seed target is an ordinary deterministic reaching target.
Survivors are the mutants a resumed run pays the most to execute again, so a
checkpoint written only at the end of the phase would lose exactly them.

A saved mutant carries the `provenance` of the run that observed its verdict
when this round resolved it from an earlier run's evidence rather than
executing anything. The run that resumes the unit did not observe the verdict
either, so it reports the reuse the interrupted run reported; the field is
optional and absent from every mutant a run executed and from every checkpoint
written before evidence was reused.

An unfinished target, package suite, or mutant is absent and therefore
implicitly pending. It is never converted to report-v1 `unknown`. At
completion, mutant accounting and the disposition inventory are rebuilt from
the current catalog plus all saved and newly executed terminal units, then
validated by the normal report identities.

## Validation and safe fallback

The base decoder rejects unknown fields, trailing JSON, duplicate identities,
invalid digests, and nonterminal saved units. Journal replay rejects unknown
fields, trailing data within a complete line, invalid identities or checksums,
a base change after a current-base record, and a record that does not carry
exactly one allowed unit. An unterminated final line is an unpublished killed
write and is ignored. A journal left after a new base was atomically published
may begin with old-base records; replay skips that stale prefix because its
state has already been compacted, then applies any current-base records appended
afterward. Replay indexes identities in one pass, sorts once, and applies the
same strict state validation as a decoded base.

Baseline target identities are checked against current discovery. The race
package list must match exactly. The mutation fingerprint is SHA-256 over sorted
mutant ID, path, package, rule, and line tuples. A probe additionally requires
the same target and suite inventories and the same ordered numeric-index,
mutant-ID, executable, and probe-capability mapping. A changed mutation
fingerprint discards only mutation state; a changed baseline inventory discards
baseline, race, and mutation state; a changed race inventory discards race and
mutation state; a changed probe inventory or mapping discards probe and mutant
results together.

Malformed or truncated JSON, a leftover temporary file, or any checkpoint
read/write failure produces a
`checkpoint-warning` and a safe cold run. It does not by itself fail the
verification.

The generated JSON Schema has `additionalProperties: false` at every object
level and is tested together with semantic validation in
`internal/checkpoint`. The persisted filename and schema identity are both
versioned so a future format cannot be mistaken for v1.

## Completion lifecycle

The optional report-v1 `resume` object records attempt count and reused target,
race-package, and mutant counts. A checkpoint remains after cancellation,
abnormal process exit, or infrastructure failure. After an ordinary
`DEFECT`, `INSUFFICIENT`, or assured result is durably written, only
`checkpoint-v1.json` and `checkpoint-journal-v1.jsonl` are deleted; a completed
cached report beside them is preserved.
