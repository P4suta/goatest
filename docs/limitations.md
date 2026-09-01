# Current limitations and release gates

This is an unreleased alpha. The implementation has strong self-tests, but
self-dogfood is not external compatibility evidence.

## Fail-closed implementation limits

- Only one main Go module is assured per run. Multiple main modules in a
  `go.work` are rejected; workspace aggregation is not implemented.
- Evidence scanning rejects symbolic links and irregular files rather than
  following confined links. Reflink/copy-on-write snapshot optimization is not
  implemented.
- Project excludes are explicit limitations. Excluded mutants are represented
  by the configured boundary, not enumerated from source that was never sent to
  the mutation catalog.
- Changeset mutation discovery conservatively includes every mutant in each
  changed non-test Go file. The pinned go-mutants API does not expose line-range
  selection, so this can over-select work but cannot omit a mutant in a selected
  changed file.
- `replay` currently accepts only mutation-backed findings with a recorded
  mutant identity. Other finding kinds are rejected instead of executing an
  unrelated mutation set.
- Cache locking is process-local. Concurrent goatest processes, target-level
  checkpoints, and interrupted-run resume are not implemented.
- Resource providers support start/ready/stop and shared/exclusive instances,
  but not health, reset, per-target scope, or log artifacts.
- The compact TTY dashboard and streaming phase-level JSONL events are not yet
  implemented; `auto` and `plain` currently use deterministic line output and
  JSONL emits the final report event.
- The HTML report supports local search/filtering but does not yet compute a
  dedicated slow-target ranking beyond searchable evidence details.
- A trace records the commands the mutation workspace runs and the mutants it
  executes. Subprocesses started outside it are not recorded: resource and
  generation providers, and the `git` invocations of changeset selection and
  report metadata. `--trace` is accepted by `verify` and `replay` alone, so a
  `fix --apply` validation runs untraced.
- The `artifact` trace event is part of `goatest-trace-v1`, but no run emits
  one yet. Nothing else in the schema is unimplemented.
- Trace directories are never pruned. `.goatest/trace/` keeps one directory per
  traced run until something outside goatest removes them, and a preserved
  command output is capped at 1 MiB per file, so a long capture is truncated
  with a marker even though its event digests the whole of it.
- Only the directory sink is reachable from the command line; the in-memory and
  fan-out sinks are in-process. There is no reader tooling for a trace either:
  it is JSON Lines for `jq` and the embedded schema, with no subcommand that
  summarizes or diffs one. See [trace v1](trace-v1.md).
- There is no benchmark/performance contract, signed release, SBOM, provenance
  bundle, GitHub Action, or tagged binary distribution yet.

## Release blockers

The first beta must retain ordinary/race tests and schema validation, add the
cross-platform CI/release pipeline, and demonstrate that no mutant disappears,
no changeset is labeled as a full assurance, and no flaky kill becomes assured.

v1.0 additionally requires compatibility runs across at least 20 external Go
repositories with at least 90% completing without source changes, plus evidence
from multiple teams that findings led to real test-gap repairs. Until those
gates are measured, documentation must not call goatest “ultimate”, a proof, or
production-ready.
