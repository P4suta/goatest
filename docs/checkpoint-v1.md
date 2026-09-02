# Interrupted assurance checkpoint v1

`assurance-checkpoint-v1` is strict scheduling state for continuing an
interrupted verification. It is never assurance evidence, never a partial
`assurance-report-v1`, and never updates `latest-any` or `latest-full`.

## Location and ownership

One exact input identity owns one checkpoint:

```text
.goatest/cache/v1/<input-digest>/checkpoint-v1.json
```

It sits beside, but is independent from, the completed cached `report.json`.
Writes use a synced temporary file and atomic rename. Cache TTL and capacity GC
remove the whole digest directory, so checkpoint state cannot outlive its cache
policy.

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
blocks inside them. The whole checkpoint is rewritten and synced at every
scheduling boundary, and this repository alone measures tens of thousands of
blocks per target, so storing them would multiply the bytes each run writes by
several orders of magnitude for state that is only a scheduling hint. A target
restored from a checkpoint is therefore routed by file for the rest of the run:
it reaches every mutant in a file it covered. That is the conservative
direction — a resumed run executes at least the work a cold run would, and the
`route` events of a trace say which targets were included that way.

## Save boundaries

A checkpoint is replaced after each complete scheduling boundary:

- build and vet have both completed;
- each baseline target has a terminal passed, skipped, failed, or not-run
  classification;
- the complete selected race phase has finished;
- after catalog validation, each mutant has one terminal result; and
- the mutation phase has completed.

An unfinished target or mutant is absent and therefore implicitly pending. It
is never converted to report-v1 `unknown`. At completion, mutant accounting and
the disposition inventory are rebuilt from the current catalog plus all saved
and newly executed terminal units, then validated by the normal report
identities.

## Validation and safe fallback

The decoder rejects unknown fields, trailing JSON, duplicate identities,
invalid digests, and nonterminal saved units. Baseline target identities are
checked against current discovery. The race package list must match exactly.
The mutation fingerprint is SHA-256 over sorted mutant ID, path, package, rule,
and line tuples. A changed mutation fingerprint discards only mutation state;
a changed baseline inventory discards baseline, race, and mutation state; a
changed race inventory discards race and mutation state.

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
`checkpoint-v1.json` is deleted; a completed cached report beside it is
preserved.
