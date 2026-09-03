# Current limitations and deferred work

This is a pre-release alpha. The implementation has strong self-tests, but
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
- Mutation routing places a mutant by the start position of its mutation, so a
  mutation spanning several lines is routed by the block that contains its
  first byte. Where the coverage toolchain and the mutation catalog disagree
  about a position — a `//line` directive is honoured by one and ignored by the
  other — the position lands either in no block, and the mutant is routed by
  its whole file as it was before blocks were read, or in a neighbouring block,
  and the mutant may be run without the test that would kill it. The second
  case is reported as a surviving mutant, never as a kill: routing errs toward
  a finding.
- Only execution the baseline coverage profiles record can narrow routing. A
  test that reaches code another way — re-executing the test binary as a
  subprocess, for instance — leaves no block behind. A mutant only that test
  covers is treated as unreached and confirmed by its package suite, where the
  test still runs; a mutant that other targets also cover is run by those
  targets alone and, if only the blind test kills it, is reported as surviving.
- Discharging a test a branch proof rules out inherits that same blind spot,
  and is fail-closed everywhere else. A test that evaluates the condition
  in-process but enters the gated body only through a subprocess leaves no
  block of the body behind: it is discharged, and if it alone kills the mutant,
  the mutant is reported as surviving — the outcome block routing already gives
  a mutant that only a blind test covers. A test that reaches the file only
  through a subprocess is not in the reaching set at all, so there is nothing to
  discharge from it, and a mutant no measured target reaches is still confirmed
  by its package suite, where the test runs. go-mutants states
  no proof over a `//line` directive, because the coverage toolchain would then
  measure the span in one file's numbering and the profile in another's. The
  span is the body's braces and is closed at both ends, which is safe under
  both cmd/cover conventions in play: Go 1.26 begins the body's block at the
  opening brace and Go 1.27 at the body's first statement, and neither begins a
  block of the surrounding code inside the braces — the block after the body
  begins one column past the closing brace or later. A proof whose coordinates
  do not describe a span the edit precedes discharges nothing.
- A baseline target restored from a checkpoint carries the files it reached but
  not the blocks inside them, so it is routed at file granularity for the rest
  of the run. A resumed run therefore executes at least the work a cold run
  would, never less. See [checkpoint v1](checkpoint-v1.md).
- Infection facts are taken from one execution of each target. A target whose
  behaviour differs between runs — a clock, a map iteration order, an unseeded
  random source — may make a mutant's site differ in the run that would kill it
  and not in the one the probe measured; the probe pass is as blind to that as
  reach is to a flaky target. A target discharged as `never-infected` is an
  execution skipped on the strength of one probe measurement, exactly as reach
  skips one on the strength of one coverage measurement, so the cost of the
  blind spot is the same as the cost of reach's: a mutant reported as surviving
  where a rerun of the discharged test might have killed it. The offline
  `proofaudit` infection layer is how the project checks the rule against the
  kills a full run actually proved. The pass also sees only
  what its probe tree records: a mutant the engine has no probe form for is
  absent from every measurement there will ever be, and is read as infected by
  every target rather than as one nothing infected. Fuzz targets are not probed
  at all, and carry no facts for the same reason.
- `replay` currently accepts only mutation-backed findings with a recorded
  mutant identity. Other finding kinds are rejected instead of executing an
  unrelated mutation set.
- Resource providers support start/ready/stop and shared/exclusive instances,
  but not health, reset, per-target scope, or log artifacts.
- The `--ui=auto` dashboard renders only on an interactive ANSI-capable
  terminal and falls back to deterministic plain lines everywhere else
  (pipes, `TERM=dumb`, `NO_COLOR`, and a legacy Windows console that refuses
  virtual-terminal processing). Its remaining-time figure is a heuristic
  projected from the average executed mutant, never a contract. The shape of a
  streamed `--ui=jsonl` progress event is diagnostic and may change between
  versions; only the final `{"type":"report",...}` line is a stable contract.
- A trace records the commands the mutation workspace runs and the mutants it
  executes. Subprocesses started outside it are not recorded: resource and
  generation providers, and the `git` invocations of changeset selection and
  report metadata. `--trace` is accepted by `verify` and `replay` alone, so a
  `fix --apply` validation runs untraced.
- The `artifact` trace event names the temporary directories `--keep-temp` kept
  and nothing else yet. Nothing else in the schema is unimplemented.
- `--keep-temp` keeps the run scratch and everything still below it, and asks
  the mutation engine to keep its snapshot, probe tree and scratch. Like
  `--trace`, it is accepted by `verify` and `replay` alone, so a `fix --apply`
  validation removes its candidate trees. The fuzz cache an original-control
  execution makes for a `-test.fuzz` argument is removed when that command
  returns regardless.
- A kept directory is named as an `artifact` event of the recording and in
  `.goatest/kept-temp-v1.json`, so a successful untraced run still leaves a
  record of what it left behind. `goatest cache status` lists the entries and
  `goatest cache gc` removes the ones older than `[cache] ttl`; a directory
  removed by hand leaves an entry that the next `gc` drops.
- The sweep that collects the leftovers of killed runs judges a directory with
  no owner marker by age alone, and spares one younger than 24 hours: it may be
  a run in progress under a binary from before the marker existed. Such a
  directory therefore survives one day longer than it needs to. The sweep never
  follows a symbolic link and never touches a name goatest did not make.
- Trace and diagnostics directories use the cache TTL and byte budget, each as
  an independent retention store. Directories kept with `--keep-temp` are bound
  by the same TTL through the ledger, but by no byte budget: a keep is a
  deliberate request, and the only bound on how much disk it may cost is how
  long it lasts. A preserved command output is capped at 1 MiB per file, so a
  long capture is truncated with a marker even though its event digests the
  whole of it.
- Only the directory sink is reachable from the command line; the in-memory and
  fan-out sinks remain in-process. `trace summary` and `trace diff` are bounded
  readers, not a query language over individual execution records. See
  [trace v1](trace-v1.md).
- Checkpoint I/O, evidence digesting, mutant accounting, and report rendering
  have local Go benchmarks. They establish a self-application baseline, not a
  cross-repository compatibility or performance contract.

## Deferred until this repository needs them

Confined symbolic-link traversal, a richer resource protocol, and aggregation
of multiple main modules in one `go.work` are intentionally deferred. The
repository does not currently exercise those layouts, so support begins only
when a concrete self-application case requires it. Packaging, signing, a
published GitHub Action, and a broad external-repository harness are outside
the current roadmap.
