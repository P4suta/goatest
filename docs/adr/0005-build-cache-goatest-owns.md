# 0005 — A build cache goatest owns, and what may write to it

## Status

Accepted, 2026-09-03. Implemented by `internal/buildcache` (the two-layer store
and the `GOCACHEPROG` server), the hidden `goatest cacheprog` subcommand in
`cmd/goatest`, the run wiring and the persist rule in `internal/assure`
(`buildCacheWorkspace`, `persistingCommand`), the `[cache] build_max_bytes` and
`build_dir` settings in `internal/config`, and the build-cache reporting and
collection in `goatest cache status|gc`.

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

5. **The base layer is bounded and collected explicitly.** `[cache]
   build_max_bytes` bounds it (10 GiB by default) and `[cache] build_dir` says
   where it is (per machine by default, because a compiled standard library is
   the same for every repository on the machine). `goatest cache status` reports
   it and `goatest cache gc` collects it, oldest-read first, keeping anything
   read within the last hour so a collection never takes a file a live build is
   about to open. A run never collects the base layer: it is shared by every
   repository on the machine, and a run that pruned it would be deciding for
   repositories it knows nothing about.

6. **The cache is never a reason to fail.** A layer that cannot be created or
   written is reported as a progress note and done without; a store that fails
   mid-protocol answers that one request with an error rather than ending the
   server. A build cache is an optimisation, and an optimisation that cannot
   start must not become a verdict.

7. **Only the composition root names the program.** The `GOCACHEPROG` value
   re-executes the goatest binary, and the go command will wait on whatever that
   value names. `cmd/goatest` is the one layer that knows the running process is
   a goatest binary; a service embedded anywhere else — a test binary running it
   in-process, an application linking it — leaves the executable empty and gets
   the toolchain's own cache. This is not a detail: resolving it one layer lower
   made every in-process test hand the go command a test binary, which sat
   printing `still waiting for GOCACHEPROG` until it failed.

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
- The layout is versioned in the directory name (`build-v1`). A later layout is
  a new directory, not a migration of somebody's disk, and the old one ages out.
- The go command opens the file a response named *after* that response, so an
  entry read moments ago must survive: an hour is both the refresh interval of
  the layer's LRU clock and the window a collection leaves alone. The bound is
  therefore soft by at most one hour of writes.
- What a run asked the cache for is reported as a `build-cache-summary` progress
  note. A reader who sees goatest go faster can see how much of it was the
  cache, which is the same rule [0004](0004-proof-layers-not-budgets.md) asks of
  a proof layer: the answer is in the recording, never in a configuration file.
