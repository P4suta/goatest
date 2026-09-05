# Run trace v1

The first trace contract is `goatest-trace-v1`. A trace is the diagnostic
exhaust of a run: the phases it passed through, the commands it executed, how
coverage and probe evidence routed each mutant, and what became of every mutant
execution.

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
| `route` | `route` | coverage and probe evidence decided a mutant's execution plan |
| `probe-exec` | `probe` | one infection probe or prepared paired-original control returned |
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
`baseline`, `graph`, `race`, `mutation-prepare`, `probe`, `mutation`, `repair`,
and `finalize`. A run uses them as a sequence rather than a nesting: entering
one ends the one before it, and the last open phase ends when the run does, so
every `phase-start` has a `phase-end` even on the cache-hit and error paths. A
run that reaches its verdict early stops partway through the list, and a round
that repeats after a promoted repair passes through it again. What a phase says
is where the run was, never what it was obliged to do. A run replaying one
mutant passes through no `probe` phase at all, because it measures nothing it
would use.

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
| `line` | the line the mutation starts on |
| `column` | the byte column it starts at on that line, 1-based |
| `reaching_targets` | the target IDs coverage or a positive probe says reach the mutation |
| `plan` | the executions the mutant will be given |
| `reason` | `coverage-reaching`, `probe-reaching`, or `unreached` |
| `granularity` | `block` or `file`: what the reaching set was decided on |
| `fallback` | `position-unknown` or `outside-blocks`, when a block decision dropped back to the file |
| `file_candidates` | how many targets cover the mutated file at all |
| `discharged` | the reaching targets a proof removed, each with the proof that removed it: `branch-never-taken` or `never-infected` |
| `probe_reaching` | the targets a positive infection measurement added despite silent coverage |
| `suite_coverage` | the synthetic identity of the passing package-suite coverage control that decided the exact mutant position |
| `suite_reached` | `true` when that control covered the position; absence beside `suite_coverage` means instrumented but uncovered |
| `suite_probe` | the synthetic identity of the measured package-suite control used by this route |
| `probed` | whether the engine compiled a probe of this mutant into the probe tree |
| `reused` | whether the run resolved this mutant from evidence an earlier run recorded instead of executing it |

`column`, `granularity`, `fallback`, `file_candidates`, `discharged`,
`probe_reaching`, `suite_coverage`, `suite_reached`, `suite_probe`, `probed`,
and `reused` are additive: a
recording made before they existed carries none of them, and each is omitted
when it is empty.

`reused` is where a run says it did not observe a verdict itself. Such a route
carries `plan: ["reused"]` and nothing else, and the recording holds no
`mutant-exec` for that mutant at all, because nothing ran: a reused route
beside an execution of the same mutant is a contradiction. The schema ties the
two fields together in both directions — a route with `reused: true` must plan
exactly `["reused"]`, and a route whose plan is `["reused"]` must say
`reused: true` — so that a reader is never told "reused" by one field and not
the other. The run the verdict came from is named in the `provenance` of that
mutant's disposition in the report.

`granularity` is what marks a route as carrying that metadata at all. On a
route that names one, an absent `file_candidates` means zero candidates; on a
route that names none, the metadata was never recorded, and a reader reports
the absence rather than a reduction of nothing. The marker is therefore
required beside the rest: a route carrying a `column`, a `file_candidates`, a
`discharged`, a `probe_reaching`, a `suite_probe`, or a `probed` without a
`granularity` would read as metadata-free while carrying some, and both the
schema and the trace reader reject it.

`discharged` is the other half of the reaching measurement. On a route of
`granularity: block`, `reaching_targets` together with the targets of
`discharged` are the targets whose covered blocks contain the mutated position:
a proof removed the discharged ones, so a discharged target never appears in
`reaching_targets` as well, and a route naming the same target on both sides is
rejected. That set is what a proof removes a target from, so a discharge is
recorded on a route of `granularity: block` and on no other: a route recording
one on any other granularity, or on none at all, is rejected by both the schema
and the trace reader. Each entry names the target and the proof that removed
it, which is `branch-never-taken` or `never-infected`. `branch-never-taken`
proves that the target never entered the body the mutated condition gates, so it
took the same branch on both programs; `never-infected` proves that the probe
pass measured the target and never saw the mutated site's value differ from the
constant the mutant puts there, so both programs ran it through identical
states. A target is
removed by one proof, so each target appears in `discharged` at most once; a
route naming the same target twice is rejected as well, since a reader counting
the entries would otherwise count that target twice. One route may carry both
reasons, because the reason is read per entry rather than per route.

goatest fills `discharged` from both proofs, applied in that order on a route
decided by block: the branch proof first, then the infection facts on what it
left. A target both would remove is therefore recorded under
`branch-never-taken`, which keeps a recording made before the second proof
existed comparable with one made after. Whichever proof removed each of them, the
entries are in run order — the order the discharged targets would have been
executed in, cheapest first — so `reaching_targets` and `discharged` are two
orderings cut from the same one. A route of `reason: coverage-reaching` with an
empty `reaching_targets` and a non-empty `discharged` is a mutant resolved
without any execution at all: coverage reached it, the proofs answered for every
target that did, and the run recorded a surviving mutant without running
anything. Such a route carries no `plan`, because the package suite behind an
empty plan would run the very tests the proofs ruled out.

`probed` says the engine compiled a probe of this mutant into the probe tree,
so the probe pass measured it. A measured target that does not name the mutant
among the `infected` of its `probe-exec` event never made the mutant's site
differ, and therefore can never observe it: routing discharges it from this
mutant's reaching set with reason `never-infected`. That discharge is the
measurement itself, so a route carrying one carries `probed` as well, and both
the schema and the trace reader reject a route that carries the discharge
without it. A mutant the engine has no probe form for carries no `probed`, and
neither does a recording made before the pass existed: an absent marker is an
absent measurement rather than a mutant nothing infected, and nothing is
discharged by it.

`probe_reaching` is the positive half of the same evidence. Every named target
also appears in `reaching_targets`: its measured probe execution named this
mutant even though coverage did not, so routing widened rather than narrowed.
Such a route has reason `probe-reaching` and carries `probed: true`; the schema
and summary reader reject the reason, target list, or marker without the other
two. The list is separate so an audit can distinguish a coverage route widened
by evidence from one coverage produced on its own.

`suite_coverage` links an otherwise-unreached route to the passing baseline
coverage command that measured the exact fallback. Its synthetic identity is
`package-suite-coverage:<import-path>`. When `suite_reached: true` is present,
one of the suite's covered blocks contained the exact mutant position and the
package suite remains the conservative route. When `suite_coverage` is present
without `suite_reached`, cmd/cover instrumented that position but the suite did
not cover it, so the package fallback is discharged for every mutation
operator. Unknown positions and positions outside all instrumented blocks carry
neither field and remain fail-closed. A positive target or suite infection is a
counterexample to negative coverage silence and prevents that silence from
removing the execution.

`suite_probe` links an otherwise-unreached route to the whole-package control
that measured its fallback. It is the synthetic `target` of a `probe-exec`, in
the form `package-suite:<import-path>`. If that execution was measured and did
not infect the probed mutant, the route has no plan: the control proved that
activating the mutant cannot change the suite. If it did infect the mutant, the
plan is `package-suite`, and the control's duration is one reference for the
mutant execution's comparative deadline. A route can carry both `suite_probe`
and `probe_reaching` when the whole-package command under the merged resource
environment was unchanged but a target-specific environment positively
infected the mutant.

A mutation spanning several lines is placed by the position it starts at, which
is the position these two fields carry.

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
whole package suite. A mutant no measured target reaches has reason `unreached`
and no `reaching_targets`. It has the package suite as its plan unless
`suite_coverage` proved its position unreached or `suite_probe` proved that
exact execution unchanged, in which case it has no plan. A mutant whose whole
reaching set was discharged has no plan as well; the proof fields distinguish
those zero-execution routes. Every other plan is derived from the targets that
reach it.

Reading `route` beside the `mutant-exec` events that follow it is how a trace
answers "why did this mutant run *that*" — the question a report can only
answer with its outcome.

### `probe`

One execution through the prepared probe tree, recorded after the engine
answered so that the execution and what it established are one line. Most are
infection probes; a `control` execution is the semantic original used for
mutation confirmation and establishes no routing fact.

| Field | Meaning |
| --- | --- |
| `target` | the target that ran, `package-suite:<import-path>` for a whole-suite probe, or `paired-control:<package>` for a paired original |
| `package` | the package the target or suite belongs to |
| `suite` | `true` for a whole-package control; absent for a top-level target |
| `control` | `true` only when this is the prepared semantic-original half of mutation confirmation |
| `args` | the test flags the execution ran with |
| `timeout_ms` | the timeout the execution was given |
| `outcome` | `measured`, `test-failed`, `timed-out`, or `unavailable` |
| `exit_code` | the exit status |
| `duration_ms` | how long it ran |
| `infected` | the mutants the target or suite made differ, by their full mutant identity |
| `error` | the error the execution failed with, if it failed |

A probe pass runs every eligible baseline target and only the package suites
where a probed mutant still needs the conservative fallback after suite
coverage. It runs them against a probe-instrumented tree where no mutant is
active, and records per mutant whether the value at its site ever differed from
the constant the mutant would put there.

Which targets. goatest probes the test and example targets, the ones the
mutation phase runs under `-test.run=^Name$`, and sends each of them the
request that phase would send for that single target: the same package, the
same `-test.run` selection followed by the run's extra test flags, the same
environment, and a comparative deadline derived from its passing baseline —
everything but the mutant, which a probe tree never activates. That is what
makes the answer a statement about the execution the mutation phase will run
rather than about some other one.
Fuzz targets receive no infection probe: the mutation phase fuzzes them beyond
the seed corpus such a probe would measure, so a measurement of the corpus
would speak for inputs it never saw. A fuzz target can still produce a
`control: true` event when a mutation needs paired confirmation; that event
asserts only that the semantic original passed.

Which suites. A package-suite probe carries `suite: true`, the synthetic target
identity `package-suite:<import-path>`, the run's extra test flags without a
`-test.run` selector, and the union of all acquired resource environments. It
is therefore the same execution as the conservative package-suite mutant
request apart from the tree and mutant activation. Its first comparative
deadline is relative to the sum of the passing baseline durations for targets
in that package; once measured, its own duration is a control for every
unreached mutant of the package. The schema requires the suite marker and a
synthetic identity; the summary reader additionally requires that identity to
name the exact `package` in the same record, so no consumer can attribute the
control to another package or count it as one top-level target.

Which controls. In a full run, every original preflight and paired kill
confirmation reuses the session's already-compiled probe binaries with no
mutant active. The package, arguments, environment, and comparative deadline
are exactly those of the mutant request; only the tree is the
semantics-preserving probe form of the original program. The synthetic identity
is `paired-control:<package>`, or `paired-control:all` for an all-package
request, and `control: true` is required. A control never carries `suite` or
`infected`: incidental probe logging is not infection evidence, `proofaudit`
ignores it, and `tracesummary` accounts for it in a separate paired-controls
block. A mutant replay deliberately prepares no probe tree and instead uses a
lazy pristine-workspace control, recorded as an ordinary `exec` event.

A target or suite the pass could not measure keeps no facts at all. A
`test-failed`, a `timed-out`, an `unavailable`, and an execution stopped by an
`error` each leave the older conservative execution in place. Only two failures
stop the pass rather than costing one execution its facts — a cancelled run,
and a session prepared without a probe tree, which is a programming error rather
than a measurement. Everything else is recorded and the pass continues.

A run replaying one mutant runs no pass and records no `probe-exec` event: it
would pay for a probe tree to measure against once, and its routing without the
measurement is the conservative one.

The `probed` field of a `route` is produced from the same prepared tree: it says the
engine compiled a probe of that mutant, which is what lets a reader tell a
mutant a measured target proved it cannot observe from one no measurement could
ever have named. Routing acts on it in both directions: it discharges a measured
coverage-reaching target whose `infected` omits the mutant, and adds a target
whose positive measurement names a mutant coverage missed. A `suite_probe`
with no infection replaces the package-suite mutant execution outright.

An execution ended in exactly one way: it reached an `outcome`, or an `error`
stopped it before one. A record carries one of the two fields and never both
or neither — a record saying neither describes no execution, and one saying
both would be an error to one reader and a measurement to another — and the
schema and the summary reader reject the other shapes.

Facts come from a `measured` execution alone. The other three outcomes, and an
execution carrying an `error` instead of an outcome, say nothing about any
mutant: a reader treats every mutant as infected by such a target rather than
as one the pass proved anything about. `infected` therefore appears beside
`measured` and nowhere else — an empty list included, since even an empty list
is the claim that the execution measured and found nothing — and both the
schema and the summary reader reject it elsewhere.

`infected` names each mutant once, by the same full identity `mutant_id`
carries, and a producer lists them in ascending catalogue order so that two
recordings of one pass list them in one order. The order is the producer's
determinism rule and not a fact a reader checks: the catalogue position is not
in the trace, so a reader without the catalogue verifies the identities and
their uniqueness alone. A measured execution with no `infected` at all is the
strongest thing the pass says about a target: that target infected nothing, so
no mutant of the pass can be observed by it.

The record describes the execution and never the tree it ran in: no path to a
probe log, no environment names, and no environment values. `args` are the test
flags the execution selected its target with.

Probe executions run concurrently and the recorder serialises them, so the
stream holds one complete line per execution, in completion order.

### `progress`

| Field | Meaning |
| --- | --- |
| `kind` | the progress kind, such as `cache-hit` or `baseline-target` |
| `detail` | the detail line that accompanies it |

These are the same notes the run reports to the progress stream, forwarded into
the recording so that a trace is a complete account of one run rather than a
half of one.

Housekeeping reports itself here and nowhere else. `temp-sweep` says what the
run reclaimed from the temporary directory before it wrote anything, and
`mutation-temp-sweep` says the same for the mutation engine's own directories;
both are silent on a machine with nothing to reclaim. `temp-unavailable` and
`kept-temp-unrecorded` say what could not be made, removed or written down.
None of them can change a verdict.

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
`run-scratch` for the one directory the run made everything else below,
`build-cache-scratch` for the layer of the build cache that would otherwise die
with the run, `baseline-scratch` for the scratch directory a round collected its
baseline in, `candidate-tree` for the isolated tree a generated candidate was
validated in, and `mutation-workspace` for each directory the mutation engine
preserved — its snapshot, its probe tree, its scratch. Those paths are absolute
and outside the repository, because that is where a temporary directory is made,
so a `path` is read as it was recorded rather than resolved against anything.
Nothing else emits an `artifact` event yet. The same paths are written to
`.goatest/kept-temp-v1.json`, which is what a successful untraced run leaves
behind; see [development](development.md) for what is kept and what is not.

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
`elapsed_ms`, and the `duration_ms` of a phase, a command, a mutant execution,
or a probe execution. JSON field order is the declaration order of the event,
not a map iteration, so two recordings of the same events differ only where
time differs.

The *stream* is a weaker promise than the fields. Baseline commands, probe
executions and mutant executions run concurrently and are recorded when they
return, so a second run of the same repository may interleave `exec`,
`probe-exec`, `mutant-exec`, and `route` events differently and hand them
different sequence numbers. What holds is the relationship a reader needs: a
mutant's `route` is always recorded before the executions it explains, and
`seq` order is the order of the file.

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
