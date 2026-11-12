# 0002 — A trace is not evidence

## Status

Accepted, 2026-09-01. Implemented by `internal/trace`, `internal/app/trace.go`,
and the trace wiring of `internal/assure`.

## Context

goatest is fail-closed about assurance. An input it cannot identify, a metadata
field it cannot reach, or an output it cannot digest ends the run with an error
rather than an optimistic verdict, because a claim that survives a missing fact
is not a claim. That discipline is why a report can be read as evidence.

Execution tracing introduces a second kind of output with the opposite failure
profile. A trace is diagnostic exhaust: a developer reads it to find out why a
run behaved the way it did, on a machine where it misbehaves, often on a CI
runner they cannot attach a debugger to. Nothing in it is a claim about the
software under test.

Applying fail-closed to that output means a full disk, a read-only directory,
or a sink that could not encode one event ends a verification that was
otherwise sound. That inverts the point of the feature: a diagnostic that makes
the tool less reliable than it was without it. The same tension has a sharper
form inside the repository — a trace written where the source snapshot reads
grows during the run, so `evidence.Scan` sees the repository change during
verification and the run dies with `repository changed during verification;
refusing stale evidence`. There, the trace does not merely risk failing the
run; it fails it by construction.

But "best effort" is where diagnostics usually start lying. A trace that
silently dropped a third of its events, or stopped early without saying so,
sends a reader hunting for the absence of a command that did in fact run.

## Decision

A trace is diagnostic exhaust and is never evidence. Concretely:

1. **It takes no part in any claim.** No trace option enters `modeIdentity`,
   the assurance inputs, or the evidence digest, so a traced and an untraced
   run of the same repository share a cache identity and reach the same
   verdict. Nothing a run decides may depend on whether it is being recorded.
2. **It never costs the run.** Sink failures are counted, never returned to the
   run. A directory that cannot be created or opened, and a close that fails,
   cost one `trace-unavailable` note on the progress stream; the run continues
   untraced.
3. **A directory the snapshot would read is refused as a trace.** `--trace=DIR`
   inside the repository but outside `.goatest` is rejected the same way an
   unopenable directory is — a note, and a run that continues. Refusing it is
   what stops it from failing the run later. `.goatest` is exempt because the
   snapshot never reads it, which is what makes the default location safe.
4. **Honesty replaces fail-closed as the discipline.** Every sink counts its
   drops, every recording ends with `events_emitted` and `events_dropped`, and
   every line is flushed as it is written. A reader can always tell a complete
   recording from a lossy one, and a killed run from a finished one.
5. **A trace is secret safe.** An exec event records environment variable names
   alone, and the recorder — not its callers — is what reduces an entry to its
   name, so no future call site can leak a value by passing one. Captured
   output is digested into the event and preserved beside the stream, so a
   reader may attach a trace and withhold the outputs.

The disabled trace is a nil `*trace.Recorder` whose every method is nil-receiver
safe. Call sites record unconditionally, which keeps the traced and untraced
call paths identical and leaves no branch for a verdict to depend on.

## Consequences

- A trace can never be cited as proof of anything. "Did this run really execute
  that target" is answered by the report and its evidence; the trace only says
  what the run appeared to do while it did it.
- A reader has two things to check before trusting a recording: that its last
  line is a `run-end`, and that `events_dropped` is zero. That obligation is
  the price of never failing the run, and it is why the accounting is a
  required field rather than an optional one.
- Because trace options are outside cache identity, tracing a warm run records
  the cache hit rather than the work the cached result stands for. Diagnosing
  the work means invalidating the entry, not adding a flag.
- A user who asks for a trace they do not get is told once, in a note, and gets
  their verdict. A workflow that must have a recording has to check for the
  directory itself; the exit code will not tell it.
- The `.goatest`-only exemption makes the repository-relative form of `--trace`
  narrow: most in-repository paths are refused. The default location and an
  absolute path outside the repository are the two ordinary answers, and CI
  should upload `.goatest/trace/`.
