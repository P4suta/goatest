# Configuration v1

`.goatest.toml` is optional and strict. Missing configuration uses
`standard-v1`, `./...`, a ten-minute execution timeout, and a cache capped at
5 GiB and 30 days. Unknown keys, malformed values, and any `version` other than
`1` are errors.

`goatest init` writes an annotated skeleton: the two active defaults, and every
section below as commented guidance, so turning a setting on is uncommenting a
line. Loading the untouched skeleton yields exactly the defaults.

## Sections

- `[project]`: `packages` and explicit `exclude` patterns. Excludes are project
  boundaries and appear as report limitations.
- `[execution]`: `build_tags`, `test_binary_args`, environment-name allowlist,
  positive `timeout`, and non-negative `jobs`. An explicit `jobs` value is the
  mutation parallelism used as written; when it is absent the run uses the
  logical CPU count capped at four, and an exclusive resource forces one job
  regardless. The decided value is announced as the `mutation-jobs` progress
  note.
- `[cache]`: non-negative `max_bytes` and positive `ttl`. The same policy is
  applied independently to exact-input cache entries, trace run directories,
  and failure-diagnostics directories. `ttl` alone also bounds the temporary
  directories a `--keep-temp` run kept: they are recorded in
  `.goatest/kept-temp-v1.json` and collected by `goatest cache gc` once they are
  older than it. No byte budget applies to those, because a keep is a request
  somebody made on purpose and the only sensible bound on it is time. Checkpoints live inside their
  exact-input cache entry and are collected with it. `build_max_bytes`
  (non-negative, 2 GiB by default) bounds the separate build cache goatest
  serves its go commands from, and `build_dir` says where that cache lives — a
  relative path is read from the repository root, and the default under the
  `goatest` CLI is a per-machine directory below the user cache directory,
  because a compiled standard library is the same for every repository on the
  machine. The two
  bounds are separate because the two stores hold different things at different
  scales: verdicts of a few kilobytes, and object files measured in gigabytes.
  Every run enforces `build_max_bytes` when it ends, so it is a bound and not
  a suggestion; see [cache maintenance](#cache-maintenance) below.
- `[resources.<capability>]`: provider `command`, positive `timeout`, one of
  `shared` or `exclusive`, and environment-name allowlist.
- `[generation]`: provider `command`, `allowed_paths`, and environment-name
  allowlist.
- `[[acceptance]]`: `id`, `reason`, RFC3339 `expires`, and optional `owner` and
  `ticket`. IDs must be unique and fields cannot contain surrounding
  whitespace.

CLI package patterns and test-binary arguments override configured defaults for
that invocation. Build tags, packages, `-short` or custom TestMain flags,
timeouts, and job limits propagate through package inspection, baseline,
coverage, mutation verification, race, and candidate validation.

goatest owns the standard `-test.*` flags that can alter routing, repetition,
selection, output protocols, or completeness. Passing them after `--` is
rejected because it could invalidate assurance evidence. `-short` (normalized
to `-test.short=true`) and `-test.parallel` are the supported standard
test-binary execution settings; custom TestMain flags remain available.

## Environment and secrets

Configuration contains environment variable names, never values. Build-affecting
Go/CGO variables and `[execution].environment` participate in exact cache
identity. Resource and generation subprocesses receive only minimum launch
variables plus the names explicitly allowed for that provider. Values are not
written to reports, diagnostics, or candidate provenance.

Provider binaries are trusted local code with the permissions of the goatest
process. All protocol input/output is size-bounded and strictly decoded, but
process isolation is not a security sandbox. Keep provider commands under the
same review and supply-chain policy as build tooling.

Protocol details are in [protocols](protocols.md).

## Cache maintenance

`goatest cache status` reports the exact-input cache, `.goatest/trace`,
`.goatest/diagnostics`, the build cache, and the temporary directory: the
leftovers of runs that were killed, and each directory a `--keep-temp` run kept.
`goatest cache gc` removes expired entries first and then the oldest entries
until each store meets its bound — `max_bytes` for the first three,
`build_max_bytes` for the build cache — and then collects those leftovers and
the keeps `ttl` has expired. Both commands report the temporary directory as
`skipped` when the process running them named none, which is every process that
is not the goatest CLI.
Verification and maintenance hold one OS advisory lock rooted at
`.goatest/cache`; a contending process reports `cache-wait`, and interrupting
that wait does not start work or run GC without the lock.

The build cache is also collected at the end of every run, with the same
policy, so `build_max_bytes` is a bound whether or not anybody types `cache gc`.
Both collections take a non-blocking lock on the layer and skip if another
process holds it, so a run and a maintenance command running side by side
simply let each other finish. A collection keeps anything read within the last
two touch intervals — two hours for the layer the machine keeps — because the
go command opens a cached file after the response that named it and a
continuously read entry's file time is refreshed only once per interval; the
bound is therefore soft by at most that window of writes.

The base layer is per machine, not per repository, because a compiled standard
library is the same for every repository on it. Two projects that configure
different `build_max_bytes` therefore share one directory and the smaller cap
wins. Removing the directory whole is always safe: it costs the next run the
work of compiling again, and the directory says so in the
`goatest-build-cache-v1` marker beside its contents. goatest will not adopt a
directory it did not make — a `build_dir` pointing at anything that already
holds other files is refused rather than collected.

The mutation evidence store, `.goatest/cache/mutation-evidence-v1.json`, is
one file beside the exact-input cache rather than an entry in it: it is
rewritten by every full run of the whole project, pruned to the mutants the
current catalogue still names, and never expires, so `cache gc` neither counts
nor removes it. It holds the kills, survivals, unreached verdicts and timeouts
a run could state a checkable claim for; nothing in it expires, and a record is
replaced when a later run contradicts it. Deleting the file only makes the next
run execute every mutant; see [the assurance contract](assurance-contract.md)
for what a run reuses from it and under which conditions.
