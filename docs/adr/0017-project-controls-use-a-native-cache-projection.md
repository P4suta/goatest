# 0017 — Project controls use a native cache projection

## Status

Accepted, 2026-09-05. Implemented by `internal/buildcache` and the command
routing in `internal/assure`.

## Context

The owned two-layer `GOCACHEPROG` cache prevents test fixtures from filling the
machine cache and preserves reusable compilation across runs. It also puts a
JSON protocol process between every short-lived child `go` command and every
cache lookup. That cost is most visible in baseline and race controls: the
project's test binary starts many child Go commands, each with a new protocol
server, even though the persistent layer already contains the standard library,
dependencies, and project packages it asks for.

Sending every command to an ordinary native `GOCACHE` is not safe. A native
cache returns paths that must remain valid until the requesting Go process
closes, while mutation commands can remain continuously concurrent for long
periods. Collecting in the middle races a live reader; merely waiting for a
quiescent instant may wait forever and makes `build_max_bytes` cease to be a
bound. The missing primitive is therefore admission control, not a larger
timeout or a second unbounded cache.

## Decision

1. **Use a hybrid cache by command lifetime.** Repository inspection and
   baseline or race compiles continue to write reusable objects through the
   persistent external cache. Mutation discovery, validation, test-binary
   compilation, direct coverage binaries, non-compile-only `go test` controls,
   prepared probe and mutant executions, and candidate validation's explicit
   instrumented verification use an ordinary run-owned native cache. A
   preparation cache miss recompiles normally. After successful preparation,
   only valid actions absent from the initial projection and their
   content-addressed objects are promoted into the persistent layer.
   Unclassified commands retain external scratch. The
   external program's required native disk backing is a separate `go-cache/`
   inside that scratch, so a broken projection cannot poison its own fallback.
2. **One projection per persistent layer, reused across runs.** The projection
   is named by the digest of the layer it mirrors and claimed with the same
   owner lock every temporary directory carries. A run that gets the lock keeps
   the directory when it finishes and leaves the objects in place for the next
   run, which then re-links nothing and rewrites nothing. A run that finds the
   lock held takes a private projection with the old per-run name and removes it
   at the end, so concurrency stays correct without a second layer of
   coordination. Collection still happens only under that lock, and no longer on
   every close: a shared projection is collected at the ordinary interval, since
   the run that closes is rarely the last one to use it.
3. **Project only at changed execution boundaries.** Before the first project
   execution, goatest converts every valid persistent action index to cmd/go's
   native v1 index and hard-links each content-addressed output object. Every
   pending package test binary is compiled before this boundary. A generation
   counter advances after a later persistent command; the next native admission
   drains active commands and refreshes exactly once. Refresh validates every
   base entry but leaves an already-current native index untouched, so it does
   not rewrite thousands of small files. The hot path never walks an unchanged
   generation. Race verification uses this deliberately: a
   multi-package `go test -race -c -o <null device>` persists link products,
   then the real race execution consumes the refreshed projection without
   writing a binary into the snapshot.
4. **Promote only the preparation delta.** The initial projection remembers its
   exact action IDs. Once discovery, validation, and test-binary compilation
   succeed, goatest scans native action names and ignores that remembered set.
   A normal run omits go-mutants' separate verification because its prepared
   baseline is the stronger control; a plan omits it because plans execute no
   repository tests. Candidate validation retains its explicit instrumented
   verification because it has no prepared baseline. Goatest validates each
   new native v1 index and regular output, hard-links new content-addressed
   objects, and publishes the external action index atomically while holding
   the base collection lock. A
   held collection defers promotion; malformed or dangling native entries are
   skipped. Promotion failure is diagnostic and costs only future compilation.
5. **Keep the projection on the base filesystem.** Hard links cannot cross a
   filesystem, so `goatest-native-cache-*` is owned beside the persistent base,
   not below the configured temporary root. It carries the same advisory owner
   pair as every other run temporary. A run and `goatest cache gc` sweep dead
   projections; `cache status` reports them; `--keep-temp` marks and records one.
6. **Create quiescence when collection or refresh is due.** Every native
   execution enters an admission gate and increments an active count. Once the
   one-minute collection interval expires, or a persistent generation must be
   projected, the gate admits no new commands and lets the already admitted
   finite batch drain. It collects least-recently-used objects or refreshes
   action indexes only at zero active, then reopens. This satisfies cmd/go's
   path-lifetime contract without serializing the normal mutation hot path. A
   deliberately kept projection receives a forced final collection; normal
   close removes it whole.
7. **Fall back, never fail verification.** An unavailable filesystem or failed
   hard link disables projection. Malformed and dangling source actions are
   skipped as misses. A native collection failure permanently closes that run's
   native gate, and subsequent commands use the continuously bounded external
   scratch. Each case changes diagnostic status only; evidence and verdict are
   unchanged.
8. **Bootstrap owned misses without adopting host storage.** The external
   program may validate the one requested action and content-addressed output
   from cmd/go's host cache, copy it into an owned layer, and answer from that
   copy. It never scans or returns the host directory. The selection and
   validation argument is [ADR 0020](0020-bootstrap-cold-preparation-with-verified-local-work.md).

## Projection soundness

`GOCACHEPROG` action and output identifiers are cmd/go's own 32-byte cache keys;
only their disk indexes differ. The native v1 index is a Go implementation
detail, not an API goatest treats as evidence. Projection validates both
identifiers and the recorded size, skips incomplete source entries, and accepts
an existing output on the initial seed only when it is the same inode as the
source. A later refresh runs only at zero active commands and may replace a
run-local output that cmd/go produced independently with the trusted base hard
link for the same content-addressed identifier. Initial projection never has
that repair authority: an unrelated pre-existing name disables native use.
A toolchain that no longer accepts the projected index observes a miss and
overwrites the run-local index; it cannot alter the persistent one or a verdict.
Action indexes are independent files, so cmd/go may replace or touch them
without mutating persistent indexes. Output contents are immutable and
content-addressed; unlinking either hard-link name leaves the other intact.

The active count covers the complete call into the command or mutation session,
not merely process startup. No native object is removed while that count is
non-zero. Once collection is due, admission remains closed until the count is
zero, so a stream of newly arriving mutations cannot starve the bound. Every
admitted command has the contract's finite command ceiling, which makes the
drain finite even for a hung test. Missing or irregular native roots and prefix
directories are rejected rather than followed.

The cache changes how a command obtains compiled bytes, not which command runs
or what counts as evidence. A miss recompiles normally. The native environment
therefore stays outside assurance and behaviour identities on the same terms as
the external cache path.

## Measured validation

On the self-verification cache, 7,920 actions and 2,402 output objects (796 MiB
logical) projected in 1.164 seconds on the same filesystem. A Go 1.26
compile-only control using that projection started 22 compiler processes and one
linker, versus 248 compiler processes and one linker from an empty native cache.
The observed walls were 5.61 and 30.53 seconds respectively, but the host had
load averages well above its eight CPUs; compiler-launch counts are the stable
comparison and wall time is supporting evidence only.

## Consequences

- Baseline, race, preparation, probe, and mutant child builds avoid the external
  protocol hot path. Successful preparation promotes its exact native delta;
  incomplete preparation cannot populate the persistent layer.
- Rebuilding the projection on every run made the first phase of a run scale
  with the size of the layer rather than the size of the work. On a layer at its
  2 GiB bound — 26968 actions, which is what this repository's own runs
  produce — a fixture verification spent a 6.06s median in a run whose whole
  cost should be a few compilations. Reusing one projection per layer, together
  with reading the layer's action records concurrently, brings that to 2.7s. The
  projection now costs what it describes: a re-link and a rewrite only for what
  actually changed.
- In an isolated empty-cache two-mutant run, preparation promoted 856 actions
  and 561 objects. A second source-identical run started 30 compiler processes
  instead of 381. Host CPU pressure exceeded 50%, so wall times are recorded as
  diagnostic evidence only and not used for the comparison.
- In a two-mutant self-package run with unchanged source and a distinct test
  argument, persisted preparation reduced that phase from 18.46 to 5.25 seconds,
  cache misses from 90 to 17, and total traced time from 23.04 to 9.43 seconds.
- In a 48-mutant `internal/processtree` run after differential refresh, the
  persisted race compile took 433 ms, native refresh plus phase overhead about
  331 ms, and the cache-hit race execution 1.324 seconds. The whole cold-input
  run completed in 16.87 seconds; mutation execution itself was 196 ms. The
  preceding implementation rewrote every projected index and spent about 2.6
  seconds in the corresponding refresh gap.
- Projection cost is proportional to small action indexes plus hard-link system
  calls, not to cached object bytes. An unchanged persistent generation is never
  walked again, and cross-filesystem copying is never attempted.
- A three-package isolated race sample took 21.39 seconds from an empty cache.
  Persisting its compile took 14.51 seconds and the cache-hit execution 10.07
  seconds: a 3.19-second first-run premium that removes about 11 seconds of
  repeated compilation on later source-compatible runs. Wall time remains
  supporting evidence because other workloads share the host.
- A projection can temporarily exceed the bound for at most one collection
  interval plus the finite batch already admitted. When the interval expires,
  the closed admission gate creates the required quiescent boundary even under
  a continuously busy mutation phase.
- Native cache statistics and fallback status are appended to the existing
  `build-cache-summary` progress note, so performance changes remain observable.
