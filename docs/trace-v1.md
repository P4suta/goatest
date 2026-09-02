# Run trace v1

The first trace contract is `goatest-trace-v1`. A trace is the diagnostic
exhaust of a run: the phases it passed through, the commands it executed, how
coverage routed each mutant, and what became of every mutant execution.

A trace is never evidence. It takes no part in a verdict and no part in the
identity a cached result is keyed on, and a trace that cannot be written costs
a warning rather than the run. See
[ADR 0002](adr/0002-trace-is-not-evidence.md) for the reasoning and
[report v1](report-v1.md) for the artifact that *is* the durable claim.

## Asking for a trace

`verify` and `replay` accept `--trace[=DIR]`. They are the commands that run
assurance and therefore the only ones that open a recording; `plan`, `doctor`,
`fix`, and `report` reject the flag rather than accepting one that would do
nothing.

| Form | Effect |
| --- | --- |
| `--trace` | record to the default directory |
| `--trace=DIR` | collect the recording in `DIR`, resolved against the repository when relative |
| `GOATEST_TRACE=1` or `true` | the same as a bare `--trace` |
| `GOATEST_TRACE=DIR` | the same as `--trace=DIR` |
| `GOATEST_TRACE` unset, empty, `0`, or `false` | no trace directory; the run records in memory |

An explicit flag always wins over the environment, so a job may ask for a trace
it cannot add a flag to and a nested job may switch an inherited one off. The
variable is read in `cmd/goatest` alone, where it becomes the flag the command
layer parses: no layer below the command line reads the environment. A
`--trace` after the `--` separator is an argument for the test binary and stays
one.

A run records into a directory of its own, named `<UTC timestamp>-<pid>` for
the moment the run started and the process that started it, inside the trace
root the flag names — `<repository>/.goatest/trace/` when it names none. That
is what lets the same `DIR` serve every run traced into it: recordings
accumulate beside each other instead of over each other. They would otherwise
collide, because everything in a recording is numbered from its first event,
so a second run into one directory would append to the first run's stream and
write its `output/<seq>.txt` over the files the first run's events digested.
The name is also what keeps two goatest processes tracing one repository out
of each other's recording.

Two directories are refused, each with one `trace-unavailable` note on the
progress stream and a run that goes on recording in memory: one that cannot
be created or opened — which includes a run directory another recording
already owns — and one inside the repository but outside `.goatest`. The
second refusal is not fastidiousness. A trace grows while the run records into
it, and the source snapshot digests the repository, so a stream written where the
snapshot reads would make the repository change during verification and cost
the run its evidence with `repository changed during verification`. Refusing
the trace is what keeps the trace from failing the run.

A directory is judged by its name and by where it lands, so a symbolic link is
not a way past that refusal: `--trace=/tmp/alias/run` is refused when
`/tmp/alias` resolves into the repository, and a name inside the repository is
refused whatever it points at. Only the part of the path that already exists
can be resolved, which is the part that decides where the directory the sink
is about to create will land.

## Recording without a flag

A run that asked for no trace still records. It keeps its last 4096 events in
a ring in memory: no file, no directory, and a bounded price a run of any
length pays once. The reason is that a failure nobody expected is exactly the
failure nobody thought to pass `--trace` for, so the recording that explains
one cannot be a recording a flag had to open in advance.

That recording is the same event stream this page describes, read back in
process rather than from a file, with two differences a reader should expect.
It holds the last events rather than all of them, and says in `events_dropped`
how many fell out. And it keeps the size and digest of a captured output
without keeping the bytes, which are never serialised into an event anyway and
would otherwise grow with the run instead of with the ring.

A refused trace directory falls back to it, which is what a refusal costs: the
file, never the account of the run.

A run that failed writes that recording out, as `trace.jsonl` in the diagnostics
bundle under `.goatest/diagnostics/<run>/`. It is this stream under this schema,
so everything that reads a trace reads that file too; a run that recorded into a
trace directory keeps its stream there instead, and its bundle names the
directory. See [development](development.md) for the rest of a bundle.

## Directory layout

```text
.goatest/trace/                        the trace root, one directory per run
  20260901T041636Z-31337/
    trace.jsonl
    output/12.txt
    output/17.txt
```

`trace.jsonl` is JSON Lines: one event object per line, in sequence order, each
line flushed as it is written. A run that hangs, is interrupted, or is killed
leaves everything it recorded readable; only the `run-end` event is missing.

`output/<seq>.txt` holds the captured output of the command recorded at that
sequence number. Output is preserved beside the stream rather than serialised
into it, and a preserved file is capped at 1 MiB and ends with a `...` marker
when it did not fit. The digest and byte count in the event always cover the
whole capture, whether or not the file was truncated.

## Event envelope

Every line has the same five envelope fields, followed by at most one payload
named after the concept it carries. The schema rejects unknown fields, so a
reader may decode strictly.

| Field | Present | Meaning |
| --- | --- | --- |
| `seq` | always | position in the recording, counting from one |
| `type` | always | which event this is, and therefore which payload it carries |
| `schema` | `run-start` only | `goatest-trace-v1` |
| `timestamp` | always | when the event was recorded, RFC 3339 in UTC, nanosecond precision |
| `elapsed_ms` | always | milliseconds since the recording started |

Sequence numbers are assigned under the same lock that hands the event to the
sink, so the order of the file and the order of `seq` are one order however
many goroutines record at once.

## Event types

| `type` | Payload | Recorded when |
| --- | --- | --- |
| `run-start` | — | the recording opens; always the first line |
| `phase-start` | `phase` | the run enters a phase |
| `phase-end` | `phase` | the phase ends, with its duration |
| `exec` | `exec` | a command of the mutation workspace returned |
| `mutant-exec` | `mutant` | one mutant execution returned |
| `route` | `route` | coverage decided a mutant's execution plan |
| `progress` | `progress` | the run reported a progress note |
| `artifact` | `artifact` | the run wrote a file |
| `run-end` | `run` | the recording closes; always the last line |

The type and the payload are one contract, in both directions: a line carries
the payload its type names and never another one, and a payload appears only on
the event type it belongs to. `schema` is the same kind of pairing, which is why
`run-start` is the only line that may carry it. The schema enforces all of it,
so a reader may switch on `type` and reach for that payload alone.

### `phase`

| Field | Meaning |
| --- | --- |
| `name` | the phase |
| `duration_ms` | how long it lasted; absent from `phase-start` |

The phases are `snapshot`, `cache-check`, `discover`, `impact`, `resources`,
`baseline`, `graph`, `race`, `mutation-prepare`, `mutation`, `repair`, and
`finalize`. A run uses them as a sequence rather than a nesting: entering one
ends the one before it, and the last open phase ends when the run does, so
every `phase-start` has a `phase-end` even on the cache-hit and error paths. A
run that reaches its verdict early stops partway through the list, and a round
that repeats after a promoted repair passes through it again. What a phase says
is where the run was, never what it was obliged to do.

### `exec`

One executed command, recorded after the engine answered so that the execution
and its result are one line.

| Field | Meaning |
| --- | --- |
| `argv` | the argument vector, verbatim |
| `dir` | the working directory |
| `env_names` | the environment variable *names*, sorted and deduplicated |
| `timeout_ms` | the timeout the command was given |
| `exit_code` | the exit status |
| `timed_out` | whether the timeout ended it |
| `duration_ms` | how long it ran |
| `output_bytes` | size of the whole captured output |
| `output_sha256` | SHA-256 of the whole captured output |
| `output_truncated` | whether the preserved file was cut at the 1 MiB cap |
| `output_path` | the preserved output, relative to the trace directory |
| `error` | the error the execution failed with, if it failed |

Every command the run executes through the mutation workspace reaches this one
event: toolchain and module inspection (`go version`, `go list`), `go vet`,
`go build`, the test baseline, the race pass, and the original-control
executions that confirm a kill. Subprocesses started elsewhere are not
recorded; see [limitations](limitations.md).

`output_path` is absent when the command produced no output, and also when the
output could not be written — preserving output is best effort, and a failure
costs the path, not the event.

### `mutant`

| Field | Meaning |
| --- | --- |
| `id` | mutant identity, the one findings and evidence are keyed on: as the engine resolved it, or as requested if it resolved none |
| `display_id` | the readable identity the engine names the mutant by |
| `package` | the package the mutant belongs to |
| `args` | the arguments the execution ran with |
| `timeout_ms` | the timeout the execution was given |
| `outcome` | `killed`, `survived`, `timed_out`, `inconclusive`, `errored`, or `not_run` |
| `killed_by` | what the engine reports killed it, when something did |
| `duration_ms` | how long the execution took |
| `error` | the error the execution failed with, if it failed |

Mutant executions run concurrently and the recorder serialises them, so the
stream holds one complete line per execution, in completion order.

### `route`

How coverage routed one mutant, recorded before the executions it explains.

| Field | Meaning |
| --- | --- |
| `mutant_id` | the mutant this plan belongs to |
| `rule` | the mutation operator that produced it |
| `path` | the mutated file |
| `line` | the mutated line |
| `column` | the mutated byte column on that line, 1-based |
| `reaching_targets` | the target IDs coverage says reach the mutation |
| `plan` | the executions the mutant will be given |
| `reason` | `coverage-reaching` or `unreached` |
| `granularity` | `block` or `file`: what the reaching set was decided on |
| `fallback` | `position-unknown` or `outside-blocks`, when a block decision dropped back to the file |
| `file_candidates` | how many targets cover the mutated file at all |

`column`, `granularity`, `fallback` and `file_candidates` are additive: a
recording made before they existed carries none of them, and each is omitted
when it is empty.

`granularity` is what marks a route as carrying that metadata at all. On a
route that names one, an absent `file_candidates` means zero candidates; on a
route that names none, the metadata was never recorded, and a reader reports
the absence rather than a reduction of nothing. The marker is therefore
required beside the rest: a route carrying a `column` or a `file_candidates`
without a `granularity` would read as metadata-free while carrying some, and
both the schema and the trace reader reject it.

The fallback ladder is three steps. A mutant whose position the engine did not
report cannot be placed in any block, so it is routed by file with fallback
`position-unknown`. A position no instrumented block contains is a gap in the
coverage the toolchain measured rather than an absence of tests, so it is
routed by file with fallback `outside-blocks`. Every other mutant is routed by
`block`, with no fallback. A route that reaches nothing while its position does
sit inside an instrumented block is a mutant in code no test executes, and is
reported as `unreached` rather than fallen back.

A fallback is why a block decision dropped back to the file, so a route that
records one records `granularity: file`. A `fallback` on any other route — on
`granularity: block`, or on a route that records no granularity at all —
contradicts itself, and both the schema and the trace reader reject it.

Two labels are worth reading carefully. A target restored from a checkpoint
carries no block evidence, so it is admitted to a reaching set by file match
while the route as a whole still records `granularity: block` — that is a
target included conservatively, not a fallback. A file no test binary was ever
linked against has no candidates at all, and its routes carry
`granularity: file` with `fallback: outside-blocks` and `file_candidates: 0`,
which is omitted from the wire as the zero it is.

A plan entry is `individual:<target>` for a target run on its own,
`batch:<package>(<count>)` for related targets of one package run together,
`fuzz:<target>` for the fuzzing of one target, and `package-suite` for the
whole package suite. A mutant
no measured target reaches has reason `unreached`, no `reaching_targets`, and
the package suite as its plan; every other plan is derived from the targets
that reach it.

Reading `route` beside the `mutant-exec` events that follow it is how a trace
answers "why did this mutant run *that*" — the question a report can only
answer with its outcome.

### `progress`

| Field | Meaning |
| --- | --- |
| `kind` | the progress kind, such as `cache-hit` or `baseline-target` |
| `detail` | the detail line that accompanies it |

These are the same notes the run reports to the progress stream, forwarded into
the recording so that a trace is a complete account of one run rather than a
half of one.

A `--ui=jsonl` run streams the same notes to stdout as
`{"type":"progress","kind":...,"detail":...,"elapsed_ms":...}` lines: same
`kind`, same `detail`, so a note reads the same wherever it is watched. The two
pipelines stay separate on purpose - the UI stream is diagnostic and carries no
schema or completeness contract, its `elapsed_ms` is stamped by the renderer,
and only the final `{"type":"report",...}` line of stdout is stable - while the
recording keeps the `events_emitted`/`events_dropped` accounting a trace is
audited by.

### `artifact`

| Field | Meaning |
| --- | --- |
| `kind` | what kind of directory or file it is |
| `path` | where it is, as the run recorded it |

A run emits one for each temporary directory `--keep-temp` asked it to keep:
`baseline-scratch` for the scratch directory a round collected its baseline in,
and `candidate-tree` for the isolated tree a generated candidate was validated
in. Those paths are absolute and outside the repository, because that is where a
temporary directory is made, so a `path` is read as it was recorded rather than
resolved against anything. Nothing else emits an `artifact` event yet. See
[development](development.md) for what is kept and what is not.

### `run`

| Field | Meaning |
| --- | --- |
| `verdict` | the verdict the runner reached, or how the run ended when it reached none |
| `error` | the error that ended it, if one did |
| `events_emitted` | events the sink kept |
| `events_dropped` | events the sink could not keep |

The accounting is never optional, because it is what tells a complete recording
from a lossy one.

The verdict is the runner's own, recorded when the run returned and before the
report scopes it, so a changeset, package, or replay run records `ASSURED`
where its report says `CHANGE_ASSURED`, `SCOPE_ASSURED`, or `RESOLVED`. The
report is the authority on the verdict; the trace only says what the run
reached.

A run that reached no verdict is closed with how it ended instead, because
those runs are exactly the ones that leave no report to say it elsewhere:
`INTERRUPTED` when a cancelled or expired context ended it, `ERROR` when
anything else did, and `UNKNOWN` for a run that returned neither a verdict nor
an error. The field is therefore never empty on a `run-end` goatest wrote.

## What is deterministic

Every field of an event is deterministic except its `timestamp`, its
`elapsed_ms`, and the `duration_ms` of a phase, a command, or a mutant
execution. JSON field order is the declaration order of the event, not a map
iteration, so two recordings of the same events differ only where time differs.

The *stream* is a weaker promise than the fields. Baseline commands and mutant
executions run concurrently and are recorded when they return, so a second run
of the same repository may interleave `exec`, `mutant-exec`, and `route` events
differently and hand them different sequence numbers. What holds is the
relationship a reader needs: a mutant's `route` is always recorded before the
executions it explains, and `seq` order is the order of the file.

A trace also depends on what the run actually did. Trace options take no part
in cache identity, so a warm run answers from the cache — and its trace records
the cache hit rather than the work the cached result stands for.

## Environment names, never values

`env_names` carries variable names alone, sorted and deduplicated, and the
recorder reduces any `NAME=VALUE` entry handed to it to its name. A trace
records which part of the environment a command could see and never what it
held. That is what makes a trace safe to attach to a bug report from a machine
holding real credentials, and it is a property of the recorder rather than of
its callers, so no future call site can leak a value by passing one.

Captured output is not filtered — it is the output of the developer's own test
suite, and truncating it to a digest would defeat the purpose. It is preserved
beside the stream instead of inside it, so a reader can quote a line of it, and
a publisher can drop the `output/` directory and keep the rest.

## Honest about loss

Recording is best effort, and best effort is only honest if the loss is
reported:

- every sink counts the events it could not keep, and the `run-end` event
  always carries `events_emitted` and `events_dropped`;
- a sink that reports its own drops is authoritative, so a bounded reader that
  discards an old event counts it even though nothing failed;
- a bounded ring keeps its last slot for the `run-end`, because the accounting
  is taken before that event is written and a ring with no room left for it
  would drop an event no line could report;
- each line is flushed as it is written, so a killed run leaves a readable
  prefix.

A reader therefore has exactly two things to check: that the last line is a
`run-end`, and that its `events_dropped` is zero. A recording without the first
was interrupted; one whose count is not zero is lossy, and says so.

## Validating and reading

The schema is `internal/trace/schema.json`, embedded in the binary and returned
by `trace.JSONSchema()`. Each line of `trace.jsonl` is one instance of it.

The built-in readers inspect default `.goatest/trace` recordings without
replaying a run:

```console
goatest trace summary
goatest trace summary 20260901T120000Z-1234
goatest trace diff 20260901T120000Z-1234 20260901T123000Z-5678
```

With no run name, `summary` selects the lexically latest confined run
directory. A missing stream is returned explicitly as `missing`; a readable
prefix without `run-end` is `incomplete-no-run-end`; sequence gaps or a
positive `events_dropped` count are `lossy`. The reader strictly rejects
unknown fields, trailing values, invalid envelopes, unexpected payloads,
duplicate `run-end` events, and non-increasing sequence numbers.

`diff` includes both summaries, then reports event, gap, and drop deltas,
verdict and `run-end` changes, per-event-type count deltas, and phase-duration
deltas. It is a read-only diagnostic comparison, not evidence and not a
verdict comparison contract.

```console
$ jq -r 'select(.type=="exec") | [.exec.duration_ms, (.exec.argv|join(" "))] | @tsv' \
    .goatest/trace/*/trace.jsonl | sort -rn | head
$ jq -r 'select(.type=="route") | [.route.mutant_id, .route.reason, (.route.plan|join(","))] | @tsv' \
    .goatest/trace/*/trace.jsonl
$ tail -n1 .goatest/trace/*/trace.jsonl | jq .run
```

`internal/trace/schema_test.go` validates recorded events against the embedded
schema, and `internal/app/trace_e2e_test.go` validates every line a real run
produced, so a field that drifts from this page fails the suite.

## Retention

Trace runs and failure-diagnostics bundles use the `[cache]` TTL and byte
budget as independent stores. Collection removes expired run directories
first, then the oldest until the store is within budget. It runs best-effort
after a recording or diagnostics bundle closes, and explicitly through
`goatest cache gc`; status for all stores is available through
`goatest cache status`. Verification and explicit GC share the repository
cache advisory lock, so collection cannot race a live recording owned by
another goatest process. Symlinks and irregular files are never followed or
removed through retention.
