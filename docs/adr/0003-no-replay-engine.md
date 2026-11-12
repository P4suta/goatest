# 0003 — No general record/replay engine

## Status

Accepted, 2026-09-01. The scripted fakes it prefers are `internal/testkit`; the
recording it declines to consume is [trace v1](../trace-v1.md).

*This record is about record/replay of process execution as a test technique.
It is unrelated to the `goatest replay ID` command, which re-runs a recorded
finding through the real toolchain and stays exactly as it is.*

## Context

A trace already records the record half of record/replay: for every command a
run executed, the argument vector, working directory, environment variable
names, timeout, exit code, timed-out flag, duration, and the captured output
preserved beside the stream; for every mutant execution, the identity,
arguments, outcome and duration. The obvious next step is to feed a recording
back into the runner so a failure reproduces without a toolchain, a repository,
or the minutes a real mutation round costs.

The suite does not need an engine to get that. Everything the runner executes
passes through two narrow interfaces — `assure.CommandWorkspace`, whose single
method is `Exec`, and `assure.MutationSession`, which adds `Catalog` — and
`internal/testkit` already answers both from prefix rule tables
(`ScriptedWorkspace`, `ScriptedSession`), fails closed on a command no rule
covers, and records what was attempted. A test states the scenario it means in
half a dozen lines, and the scenario is legible in the test.

An engine, by contrast, is a standing obligation. It has to decide when a
recorded command matches a command a changed runner issues, what to do when the
runner asks something the recording never saw, how a recording is versioned
against the schema that produced it, and how a second execution path through
the runner stays honest as the first one changes. That is a second
implementation of what the fakes already do, in a repository whose rule is to
add no dependencies and few seams, paid for on every future change to a command
line.

There is also a gap that no amount of engineering closes: a trace records
environment variable *names* and never values (see
[ADR 0002](0002-trace-is-not-evidence.md)), and preserved output is capped at
1 MiB. A recording is a faithful description of an execution, not a substitute
for the environment that produced it.

## Decision

No general record/replay engine, and no replay execution path in the runner.
Recording is what the trace does; standing in for an execution is what the
hand-written scripted fakes do. Traces are read by people.

The event schema keeps the parts a future bridge would need lossless anyway:
`exec.argv` and `mutant.args` verbatim, `exec.dir`, the exit code, the
timed-out flag and the duration as recorded, and the captured output preserved
as a file rather than summarized into the event. Any change that compresses,
elides, or normalizes those fields revisits this record.

**Revisit when** transcribing a trace into scripted rules by hand becomes a
recurring cost rather than an occasional one. The answer then is the smallest
thing that could work: `testkit.WorkspaceFromTrace`, which reads a
`trace.jsonl` and registers one `ScriptedWorkspace` rule per `exec` event —
keyed on the argv, answering with the recorded exit code and duration and the
bytes of its preserved output file — plus the `ScriptedSession` equivalent over
`mutant-exec` events. That is a testkit helper on top of the existing fakes,
with no engine, no matching policy beyond the prefix table the fakes already
have, and no change to the runner.

## Consequences

- Test doubles stay explicit. A reader of a test sees the workspace's answers
  next to the assertion about them, instead of a recorded blob and a matching
  policy they must reconstruct.
- Traces stay a debugging artifact rather than a test input, so no golden
  recording has to be re-recorded when a command line changes, and no test
  fails because a recording aged.
- Reproducing a complicated real failure in a test means reading a trace and
  writing rules by hand. That is the cost this record accepts, and the trigger
  that would reopen it.
- The trace schema carries an obligation it would not otherwise have: argv,
  args, and preserved output stay lossless for a use nothing makes of them yet.
