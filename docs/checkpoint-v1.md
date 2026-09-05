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
contract, package/scope options, build tags, test-binary arguments, timeouts,
and mutation settings to compute the input digest. There is no CLI resume flag:
only a checkpoint under the newly computed, identical digest is considered.

Configured resource providers disable checkpoint reuse because their runtime
state is not captured. Only repair round zero may reuse a checkpoint. A corpus
promotion or generated source change deletes and disables the old checkpoint
before the next round.

A saved baseline target carries the files it reached and not the coverage
blocks inside them. This repository alone measures some four thousand blocks
for each of several hundred targets, so storing them would multiply durable
checkpoint bytes by orders of magnitude for state that is only a scheduling
hint. A target
restored from a checkpoint is therefore routed by file for the rest of the run:
it reaches every mutant in a file it covered. That is the conservative
direction — a resumed run executes at least the work a cold run would, and the
`route` events of a trace say which targets were included that way.

The target and package-suite infection facts of the probe pass, including their
control durations, are in memory only for the same reason and under the same
rule. A checkpoint carries none of them. A resumed run may establish fresh
facts by repeating the probe phase; any route that has no fresh measurement is
treated conservatively, as infecting rather than as proved unchanged. The
probe pass itself is not a save boundary and adds no field to `checkpoint-v1`.

Repository-read observation is different because a resumed mutation verdict
must retain the input boundary established by its baseline. A saved target
therefore carries whether observation covered its package and whether it
selected the whole-tree key. An older checkpoint has neither optional field;
when its package is a current reader candidate, that absence is conservatively
restored as whole-tree rather than interpreted as a measured narrow result.

## Save boundaries

The complete base checkpoint is atomically replaced at each structural phase
boundary:

- build and vet have both completed;
- the baseline phase has completed;
- the complete selected race phase has finished;
- mutation catalog validation or invalidation has completed; and
- the mutation phase has completed.

Inside the baseline and mutation phases, each terminal unit is instead one
append-only journal record. A baseline target is appended after its passed,
skipped, failed, or not-run classification is complete. A mutant is appended
the moment nothing more can be learned about it. Each record names the input
digest and the SHA-256 digest of the base document it extends, contains exactly
one unit, carries its own checksum, ends in a newline commit marker, and is
synced before publication returns. This retains per-unit durability without
rewriting all earlier units after every process.

Baseline workers may finish out of order, but only the longest fully measured
prefix is published, in target order. A worker never writes checkpoint state
itself. The completed base checkpoint is consequently byte-identical to serial
execution, and interruption can expose only complete target journal records,
never a partially decoded coverage profile or repository observation.

A mutant is terminal the moment nothing more can be learned about it, not when
the phase around it ends. A mutation every reaching test passed is saved as a
surviving mutant right there, and so is one whose every reaching test a branch
proof discharged, which is terminal without having been executed at all; only
one a fuzz target reaches waits, for the fuzzing that may still kill it.
Survivors are the mutants a resumed run pays the most to execute again, so a
checkpoint written only at the end of the phase would lose exactly them.

A saved mutant carries the `provenance` of the run that observed its verdict
when this round resolved it from an earlier run's evidence rather than
executing anything. The run that resumes the unit did not observe the verdict
either, so it reports the reuse the interrupted run reported; the field is
optional and absent from every mutant a run executed and from every checkpoint
written before evidence was reused.

An unfinished target or mutant is absent and therefore implicitly pending. It
is never converted to report-v1 `unknown`. At completion, mutant accounting and
the disposition inventory are rebuilt from the current catalog plus all saved
and newly executed terminal units, then validated by the normal report
identities.

## Validation and safe fallback

The base decoder rejects unknown fields, trailing JSON, duplicate identities,
invalid digests, and nonterminal saved units. Journal replay rejects unknown
fields, trailing data within a complete line, invalid identities or checksums,
mixed base digests, and a record that does not carry exactly one allowed unit.
An unterminated final line is an unpublished killed write and is ignored. A
journal left after a new base was atomically published is ignored when its
first record names the old base digest; the state it contains has already been
compacted into the new base. Replay indexes identities in one pass, sorts once,
and applies the same strict state validation as a decoded base.

Baseline target identities are checked against current discovery. The race
package list must match exactly. The mutation fingerprint is SHA-256 over sorted
mutant ID, path, package, rule, and line tuples. A changed mutation fingerprint
discards only mutation state; a changed baseline inventory discards baseline,
race, and mutation state; a changed race inventory discards race and mutation
state.

Saved repair candidates must still load from the candidate store. A missing
candidate discards mutation state rather than consuming incomplete evidence.
Malformed or truncated JSON, a leftover temporary file, an unavailable
artifact, or any checkpoint read/write failure produces a
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
