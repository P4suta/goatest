# 0009 — Parallel measurement, serial commit

## Status

Accepted, 2026-09-05. Implemented by the bounded baseline target and package
suite schedulers in `internal/assure`, concurrent original-control workspaces,
and concurrent pre-preparation execution in go-mutants.

## Context

A clean dogfood run spent 1,013.739 seconds in baseline. Its 1,088 independent
target controls accounted for 803.657 seconds of child-process time, while the
30 package test-binary compiles accounted for only 51.068 seconds. The run was
configured for eight jobs, but every baseline target still ran serially:
goatest had no scheduler there, and the frozen go-mutants workspace held its
state mutex for the lifetime of every command.

The targets are logically independent measurements, but merely starting them
in goroutines would not be a deterministic design. Process completion order is
chosen by the operating system. If workers appended directly to the report,
wrote checkpoint state, or returned the first error they happened to observe,
two runs of the same program could publish different ordering and even name a
different infrastructure failure.

Concurrent controls also need the same safety boundary as concurrent mutation
execution. Temporary directories must not be shared, preparation must not race
an unfinished baseline command, and a test that changes the frozen repository
must not silently change the program mutation discovery measures.

## Decision

1. **Measurement is parallel; publication is serial.** Within each compiled
   package, at most `[execution].jobs` workers execute target controls. A worker
   owns only its target's command, coverage profile, repository log, and result
   slot. It never appends to the report or writes a checkpoint.
2. **Input order is the commit order.** The coordinator retains out-of-order
   completions and commits only the longest completed prefix of the package's
   target list. Evidence, findings, inventory, coverage merging, and checkpoint
   updates therefore have exactly the serial order.
3. **The earliest target owns an error.** All already-started workers are
   joined, then the error belonging to the earliest target in input order is
   returned. Scheduler timing cannot decide which failure the user sees.
4. **The existing resource limit is the one limit.** The job count shared by
   baseline, probe, and mutation is derived once. A configured exclusive
   resource forces it to one; a shared resource explicitly permits concurrent
   users. The direct `CollectBaseline` API retains serial execution when its
   job count is omitted.
5. **The frozen workspace admits concurrent controls safely.** Each
   go-mutants `Workspace.Exec` call receives a private temporary directory.
   `Prepare` and `Close` take the exclusive side of the execution lock and wait
   for every control. Before discovery, `Prepare` re-digests the snapshot and
   refuses any path a control added, removed, or changed.
6. **Package controls overlap by package.** Whole-suite coverage controls run
   in deterministic import-path slots after target controls have supplied their
   duration references. Exact uninstrumented controls use a lazy second frozen
   workspace: independent commands take shared execution ownership, opening is
   performed once, and close takes exclusive ownership after all commands end.
   Command results are memoized by package, arguments, and environment, so
   concurrent mutants never duplicate the same original preflight.

## Consequences

- The amount and meaning of baseline evidence are unchanged; only independent
  process wall time overlaps.
- Report bytes, completed checkpoint bytes, and error selection are independent
  of completion order. Diagnostic trace execution events remain in completion
  order, as they already are for probe and mutation workers.
- A crash may leave the same kind of fully classified prefix checkpoint as a
  serial run. No partially measured target is ever resumable.
- CPU and I/O contention can reduce the ideal eightfold gain. A timeout under
  that contention is still inconclusive, and the control-relative timeout rule
  remains fail-closed.
- A suite that depends on undeclared shared external state may expose its own
  nondeterminism sooner. Such state belongs in a configured resource; an
  exclusive resource restores serial execution without changing code.
