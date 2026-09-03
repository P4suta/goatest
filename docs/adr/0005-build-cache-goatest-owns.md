# 0005 — A build cache goatest owns, and what may write to it

## Status

Accepted, 2026-09-03; revised 2026-09-04 after review (decisions 5 to 8).
Implemented by `internal/buildcache` (the two-layer store, the `GOCACHEPROG`
server, and the locked collection), the hidden `goatest cacheprog` subcommand in
`cmd/goatest`, the run wiring and the persist rule in `internal/assure`
(`buildCacheWorkspace`, `persistingCommand`, `collectRunBuildCache`), the
`[cache] build_max_bytes` and `build_dir` settings in `internal/config`, and the
build-cache reporting and collection in `goatest cache status|gc`.

## Context

A verification of this repository leaves gigabytes behind. The measured figure
was about 5.5 GB per hour in `~/.cache/go-build`, and a single run added around
14 GB; three agents sharing one 457 GB disk filled it. The garbage is not
mutants — go-mutants compiles a tree per run, not per mutant. It is the go
commands the *test suites themselves* run: goatest's own suite compiles fixture
modules, and so does any suite with a golden build, a `go list` under test, or a
generated package. Each of those compiles into a directory that exists only for
that test, so its cache entries are addressed by a path nothing will ever ask
for again, and the toolchain's cache keeps them for its own TTL regardless.

Two things follow. The disk fills with entries that can never be hit again. And
the work that *would* be hit again — the standard library, the dependencies, the
project's own packages — is evicted by that garbage, so a run recompiles what
the machine already compiled an hour ago. The first is a housekeeping problem;
the second is a speed problem, and speed is a product property here (see
[0004](0004-proof-layers-not-budgets.md): the answer to a slow run is never a
smaller run).

The go command has exactly the hook this needs. `GOCACHEPROG` names a program it
starts and speaks a small JSON protocol to, and every go command below it — the
ones goatest issues, and the ones a test suite's children issue — inherits the
variable and asks the same program. That makes the decision about which
compilations the machine keeps a decision goatest can make, instead of one the
toolchain makes by TTL.

## Decision

1. **goatest serves its own build cache, in two layers.** A *base* layer belongs
   to the machine and survives between runs; a *scratch* layer belongs to one
   run and is removed when it ends. Reads resolve scratch and then base, so a
   run always sees its own work first and falls back on what the machine already
   compiled. `goatest cacheprog` is the server, started by the go command and
   never by a person, and it is absent from the help text for that reason.

2. **Only a command that compiles or lists may write to the base layer.** The
   rule is exactly: `go vet`, `go build`, `go list`, `go version`, and a `go
   test` that is compiled and not run (`-c`). Concretely that is the baseline's
   vet, build and test-binary compile, and the run's `go version`, `go list
   -json ./...`, `go list -m -json all`, and the selected-package listing.

3. **Nothing that runs the project's tests may.** The baseline target runs, the
   race verification, the original-mutation control, the go-mutants session, and
   candidate validation all write to the run's scratch layer, which dies with
   the run. This is the load-bearing half. A baseline target is the project's
   own test binary wrapped in `go tool test2json`, so its argument list begins
   with the go binary exactly as a compile does — and it is precisely the
   command whose children produce the throwaway fixture builds. Were it to
   persist, every fixture package would be written into the base layer and would
   evict the standard library the layer exists to hold: the cache would grow
   without bound and get slower the more it was used. The rule therefore reads
   the *subcommand*, never the executable.

4. **The rule lives in one place and is pinned by a test.** `persistingCommand`
   is the whole policy, `buildCacheWorkspace` is the only thing that applies it,
   and only a run's workspace is wrapped.
   `TestOnlyCommandsThatCompileOrListPersistToTheBaseLayer` states every command
   goatest issues and which side of the rule it falls on, built from the same
   argument-list builders the run uses, so a change to what goatest runs is a
   change that test sees.

5. **Every run bounds the base layer, and the bound is small.** `[cache]
   build_max_bytes` bounds it — 2 GiB by default, because the cache is one
   directory on a disk shared with everything else the developer does — and
   `[cache] build_dir` says where it is (per machine by default, because a
   compiled standard library is the same for every repository on the machine).
   A run collects the layer when it ends, oldest-read first, under a
   non-blocking lock on the layer; `goatest cache status` reports it and
   `goatest cache gc` runs the same collection on demand. A cap applied only by
   a command a developer remembers to type is not a cap, which is what an
   earlier draft of this decision got wrong.

   That the layer is shared by every repository on the machine is what the lock
   and the idle window are for, not a reason to leave it uncollected. A run
   that finds another process collecting yields to it and says nothing: the
   layer ends up bounded either way. What protects the builds of those other
   repositories is `MinIdle`, which spares by construction everything read
   inside the last touch interval. One consequence is worth stating plainly:
   the layer is shared, so the smallest `build_max_bytes` among the
   repositories that actually run on the machine is the one that wins.

6. **`MinIdle` is at least two touch intervals, and the layer says so.** A
   collection may only remove an entry whose last touch is older than
   `MinIdle`, and a read refreshes an entry's file time at most once per touch
   interval — otherwise every cache hit would be a write. So an entry a live
   build is reading continuously already carries a file time up to one whole
   interval stale; that is the first interval. The second is the window the go
   command needs after the response: it opens the file a response named *after*
   that response, so an entry served moments ago must still be there.

   `Layer.MinIdle` derives it from the layer's own touch interval rather than
   leaving each caller to restate it. The intervals are per layer — an hour for
   the layer the machine keeps, a minute for a run's own scratch, whose whole
   life is one run and whose go commands are bounded in minutes — and the
   inequality holds for each.

7. **A layer is a directory goatest made, and the marker proves it.**
   `Prepare` refuses a directory that exists, holds files, and carries none of
   goatest's own names, and refuses it without writing anything into it. The
   marker is `goatest-build-cache-v1`, not a `README`: a `README` is a file a
   project already keeps, and a `build_dir` typed as a home or project
   directory is exactly the mistake that must not become a directory goatest
   collects and removes files from. An absent directory, an empty one, and one
   holding only goatest's own names are all claimed.

   `Prepare` runs once per run, in the run. The served process creates only the
   directories its own writes need: there is one of them per go command and a
   run issues thousands, so a stat, a readdir and an fsynced marker apiece
   would be pure cost. It never rewrites a marker and never claims a directory,
   because adopting one is the run's decision and not a go command's.

8. **The scratch layer is bounded by size, not by age, and not on every
   close.** A run may compile a package in one phase and want it in a later
   one, so removing an entry because nothing read it for a while only buys a
   recompile; removing the least recently read once the layer is over
   `build_max_bytes` is the cost the project agreed to. The collection runs at
   most once per minute, gated by the same lock and by the file time of the
   record inside the layer, because there is one served process per go command
   and collecting on each close is work proportional to the square of the run.

9. **The cache is never a reason to fail.** A layer that cannot be created or
   written is reported as a progress note and done without; a store that fails
   mid-protocol answers that one request with an error rather than ending the
   server. A build cache is an optimisation, and an optimisation that cannot
   start must not become a verdict.

10. **Only the composition root names the program.** The `GOCACHEPROG` value
    re-executes the goatest binary, and the go command will wait on whatever
    that value names. `cmd/goatest` is the one layer that knows the running
    process is a goatest binary; a service embedded anywhere else — a test
    binary running it in-process, an application linking it — leaves the
    executable empty and gets the toolchain's own cache. This is not a detail:
    resolving it one layer lower made every in-process test hand the go command
    a test binary, which sat printing `still waiting for GOCACHEPROG` until it
    failed.

    Where the cache *lives* is resolved separately, and does not need the
    executable at all. It is a property of the machine and the project, and
    `cache status` and `cache gc` need it whether or not this process could
    serve the cache; tying the two together made maintenance silently report an
    empty cache. Separately, though, is not lower: the machine's cache root is
    named only by the composition root as well, because `cache status` inspects
    that directory and `cache gc` and every closing run collect it — so an
    embedded service or a test binary that resolved it for itself would be
    deleting entries out of the running developer's own build cache. A process
    that names no cache root keeps its layer where the project configured one,
    or keeps none.

## Consequences

- The cache program is a second entry point into the goatest binary, on the
  process's own streams. It parses its layers from its command line and reads no
  configuration, no repository, and no environment: the go command that started
  it was given everything already.
- `GOCACHEPROG` is deliberately not in `buildEnvironmentNames`, so it takes no
  part in the assurance digest. It names a per-run scratch directory, and a
  cache identity that changed every run would never hit.
- A run's scratch layer is a temporary directory like the baseline scratch, so
  `--keep-temp` keeps it and records it as a `build-cache-scratch` artifact.
- The layout is versioned twice over: in the directory name (`build-v1`) and in
  the marker name (`goatest-build-cache-v1`). A later layout is a new
  directory, not a migration of somebody's disk, and the old one ages out.
- The bound is soft by at most one `MinIdle` of writes, which is two touch
  intervals: everything read inside that window is spared however far over the
  cap the layer is, and the next collection takes the rest.
- The base layer is per machine, so two repositories with different
  `build_max_bytes` share one directory and the smaller cap wins. That is the
  right trade — the layer is mostly compiled standard library, which is the
  same for both — but it means a project cannot reserve space in it.
- A build of the identical source at a different absolute path misses for the
  project's own packages, because the go command hashes the package directory
  into the action ID unless `-trimpath` is set. goatest does not set it: that
  would change the binaries under verification, and a verdict has to be about
  the build the project actually makes. The standard library closure — most of
  the layer — is path independent and hits regardless, and the effort goes
  instead into giving go-mutants a snapshot directory that is stable per
  repository root, so successive runs of one repository hit each other.
- What a run asked the cache for is reported as a `build-cache-summary` progress
  note, and what its final collection removed as `build-cache-collected`. A reader who sees goatest go faster can see how much of it was the
  cache, which is the same rule [0004](0004-proof-layers-not-budgets.md) asks of
  a proof layer: the answer is in the recording, never in a configuration file.
