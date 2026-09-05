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
- Coverage-blind execution can be recovered only from a positive probe fact. A
  test that reaches code another way — re-executing the test binary as a
  subprocess, for instance — may leave no block behind. When its measured probe
  execution names the mutant, routing adds that target despite the silent
  profile. If the probe is unmeasured, the mutant has no probe form, or the
  child does not participate in the probe runtime, goatest cannot invent that
  reachability. An otherwise-unreached mutant still has its exact package-suite
  fallback measured first with coverage and, where useful, infection; anything
  neither control answers receives a prepared semantic-original preflight and
  is then executed with the mutant. A blind target omitted from a non-empty
  route remains a possible surviving mutant rather than a manufactured kill.
- Negative package-suite coverage is one measured execution, not a liveness
  theorem. It discharges a fallback only when the exact mutant position lies in
  a block cmd/cover instrumented but the passing suite did not cover. A clock,
  map order, unseeded random source, or external state may make another run
  reach a position the control did not. A subprocess or other execution that
  does not contribute to the profile can be invisible, and coverage
  instrumentation can itself perturb timing or scheduling. Unknown positions,
  gaps between instrumented blocks, failed controls, and missing profiles are
  kept; a positive target or suite infection overrides contradictory coverage
  silence. Remaining fallbacks run the prepared semantic-original package
  first and then the mutant, so the proof never substitutes a guess for an
  unknown. `proofaudit` independently checks the rule against attributable
  package-suite kills in full recordings, reporting missing or conflicting
  profiles as unverifiable.
- Discharging a test a branch proof rules out inherits that same blind spot,
  and is fail-closed everywhere else. A test that evaluates the condition
  in-process but enters the gated body only through a subprocess leaves no
  block of the body behind: it is discharged, and if it alone kills the mutant,
  the mutant is reported as surviving — the outcome block routing already gives
  a mutant that only a blind test covers. A test that reaches the file only
  through a subprocess is not in the reaching set at all, so there is nothing to
  discharge from it unless its positive target probe recovers the execution. A
  mutant no measured target reaches is still settled by its measured or
  executed package suite. go-mutants states
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
- Infection facts are taken from one execution of each eligible target and each
  package suite for which infection can still answer an unresolved mutant. A
  target or suite whose behaviour differs between runs — a clock, a
  map iteration order, an unseeded
  random source — may make a mutant's site differ in the run that would kill it
  and not in the one the probe measured; the probe pass is as blind to that as
  reach is to a flaky target. A target discharged as `never-infected` is an
  execution skipped on the strength of one probe measurement, exactly as reach
  skips one on the strength of one coverage measurement, so the cost of the
  blind spot is the same as the cost of reach's: a mutant reported as surviving
  where a rerun of the discharged test might have killed it. The offline
  `proofaudit` infection layer is how the project checks the rule against the
  kills a full run actually proved. A package suite proved uninfected has the
  same single-observation limitation: nondeterminism can make a later mutant
  execution take a path its control did not. The pass also sees only
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
  removed by hand leaves an entry that the next `gc` drops. A collection removes
  a directory only when the directory's own marker says it was kept, so one
  whose marker was deleted is never collected: it stays listed as `unverified`
  and has to be removed by hand.
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
- `reports/runs` is bound by a count and not by age or size: the newest
  `[reports] keep` runs, twenty by default, plus whatever `latest-any.json` and
  `latest-full.json` point at. A run older than the newest twenty and referenced
  by neither index is gone, however recently anybody looked at it and however
  small it was, and `goatest report --run` of it then says it was collected or
  never written. Copy `reports/runs/<run-id>` elsewhere to keep one.
- `.goatest/candidates` and `.goatest/patches` are bound by the cache TTL and
  byte budget. A collected candidate cannot be applied by `goatest fix`, and an
  interrupted run whose checkpoint recorded one discards its saved mutation work
  and starts that phase again. That is why the candidate store is left alone
  entirely while any checkpoint exists, which also means a checkpoint nobody
  ever resumes holds the whole store past its budget until `cache gc` runs
  without one.
- Only the directory sink is reachable from the command line; the in-memory and
  fan-out sinks remain in-process. `trace summary` and `trace diff` are bounded
  readers, not a query language over individual execution records. See
  [trace v1](trace-v1.md).
- Reusing a verdict across runs rests on a behaviour key built from what a test
  binary links and reads. A syntactic list of directory APIs selects candidates;
  their baseline and evidence-producing mutant runs are narrowed with Go's test
  action log, and any observed repository input outside the ordinary closure —
  or any missing/ambiguous log — selects a whole-tree key. The log covers
  operations made through package `os` after `testing.M.Run` starts. Direct
  candidate calls in package initialization or `TestMain`, and generic `io/fs`
  calls with an unknown backing store, therefore remain statically whole-tree.
  Reads made only through raw syscalls, a subprocess, a helper package the
  syntactic list does not select, or an uninstrumented pre-`M.Run` helper remain
  outside the model and can outlive a file they depended on. This is an
  execution-observation boundary, not an exclusion: every mutant is still
  tested, and uncertain evidence is widened rather than trusted.
- A finite cancellation ceiling remains. Arbitrary mutated code can loop,
  deadlock, recurse without bound, or simply compute for a long time, and no
  finite supervisor can distinguish every slow terminating execution from a
  nonterminating one. Ordinary mutation deadlines are therefore comparative to
  same-run passing controls rather than project-independent waits, and their
  expiration is inconclusive rather than a verdict. When a package fallback
  has no positive duration sample, the legacy 30-second-floor calibration may
  still be paid once by its memoized prepared original preflight; compilation
  is outside that deadline and it is not paid independently by every mutant.
  Replay has no prepared probe tree and can still pay for its pristine fallback
  compilation. Fuzz campaigns retain a separate fixed safety bound because a
  seed-corpus duration does not predict the configured search.
- A reused timeout keeps its finding rather than resolving anything, so it can
  only ever cost a run work it did not need to do, never assurance. Its
  condition is existential — the target time ran out under still reaches the
  mutant unchanged — so a run reuses a timeout even when other targets have
  since joined the reaching set, and a mutant one of those new targets would
  now kill keeps its `mutation-timeout` finding until something re-runs it.
  That is the fail-closed direction, and it is deliberate: nothing is resolved
  by a reuse that never resolved anything. It is also never re-derived on its
  own: `goatest replay <finding-id>` bypasses evidence entirely and is the way
  to run one again.
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
