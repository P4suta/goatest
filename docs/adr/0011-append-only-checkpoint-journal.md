# 0011 — Append-only checkpoint journal

## Status

Accepted, 2026-09-05. Implemented by `internal/cache` and the optional journal
interface consumed by the assurance coordinator.

## Context

An interrupted run must durably publish every completely classified baseline
target and mutant. Those are precisely the expensive units a resumed cold run
must not repeat. The original implementation achieved that property by
serialising, writing, syncing, and atomically renaming the complete growing
checkpoint document after every unit.

That algorithm is crash-safe but quadratic in cumulative bytes. Unit one is
written for every later unit, unit two for every later unit, and so on. At the
scale of a cold full mutation run, checkpoint publication became observable
wall time and serialised otherwise independent workers after their executions
had finished. Removing per-unit durability would improve speed by discarding
the exact interruption guarantee the checkpoint exists to provide.

## Decision

1. **Keep the strict base document.** `checkpoint-v1.json` remains the complete,
   schema-validated state at phase boundaries. It is written through a synced
   temporary file and atomic rename, as before.
2. **Append completed hot-path units.** Between phase boundaries, a production
   cache appends one JSON object per complete baseline target or mutant to
   `checkpoint-journal-v1.jsonl`. Each record carries the schema identity,
   input digest, SHA-256 digest of the base document it extends, exactly one
   unit, and a checksum over the record with the checksum field cleared.
3. **Make newline the record commit marker.** One encoded record and its final
   newline are appended, the journal is synced, and only then does the call
   return. Replay accepts every complete checksummed line. An unterminated tail
   is a killed write and is ignored; a malformed complete line is corruption
   and fails the checkpoint closed.
4. **Compact at phase boundaries.** Any structural transition — build/vet,
   completed baseline, race, catalog creation or invalidation, and completed
   mutation — publishes a new full base document. The successful rename makes
   that base authoritative, then the old journal is removed. If the process
   dies between those steps, the first old journal record's base digest differs
   and replay ignores the entire stale journal because its state is already in
   the new base.
5. **Replay in linear time and canonicalise once.** Replay indexes existing
   target and mutant identities, replaces or appends each unit in one pass, and
   sorts each completed collection once. Mutation workers likewise append to
   in-memory state without sorting the growing slice; compaction performs the
   canonical sort. Completed checkpoint bytes therefore remain deterministic
   without an `O(n log n)` operation at every unit.
6. **Preserve the embedding contract.** The append interface is optional.
   A cache implementation that provides only the original full-state method
   still receives an atomic full checkpoint at each scheduling boundary and
   has exactly the old semantics.

## Crash and concurrency invariants

The input digest prevents one exact run from consuming another's state. The
base digest prevents records from being replayed onto a different phase or a
newly compacted base. The record checksum detects complete-line corruption.
The strict checkpoint validator runs again after replay, so valid JSON cannot
smuggle an invalid target, mutant, phase, or accounting state into a resume.

Within one process, cache operations and coordinator state transitions are
serialised. Across processes, the repository cache advisory lock spans
checkpoint access through report persistence. No supported execution has two
writers for the same journal. The journal does not weaken result ordering:
baseline workers are still committed in target order, while mutation results
are identity-keyed and sorted when compacted.

## Consequences

- Per-unit durable publication writes the size of the new unit rather than the
  size of all work completed so far. Total hot-path checkpoint I/O is linear in
  completed state instead of quadratic.
- A process killed during an append loses at most the incomplete record whose
  newline was not committed. Earlier complete records remain resumable.
- A malformed complete record disables and removes interrupted state for that
  run, then verification starts cold. The journal can never be interpreted as
  assurance evidence or as a partial report.
- The base document remains the portable public checkpoint shape. The journal
  is an internal durability mechanism with its own explicit version and can be
  replaced without changing report v1.
