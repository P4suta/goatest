# 0020 — Bootstrap cold preparation with verified local work

## Status

Accepted, 2026-09-06. Implemented by `internal/app`, `internal/assure`,
`internal/buildcache`, `internal/mutationbridge`, and go-mutants preparation.

## Context

A first verification of one package still paid for repository-wide mutation
discovery and rebuilt dependencies already present in cmd/go's host cache. The
owned persistent cache could help only after goatest had populated it, so the
first useful result remained much slower than later runs.

Copying a host cache wholesale is neither bounded nor necessary. Returning a
path owned by that cache would also let unrelated collection invalidate a live
command. Mutation discovery has a separate overbreadth: packages used to build
selected test binaries are not necessarily packages whose source is in the
mutation boundary.

## Decision

1. Mutation preparation receives an explicit discovery package set independent
   of the packages whose test binaries it builds. An explicit verification uses
   the exact candidate package directories selected by include scope. With no
   include scope, explicit test packages are the discovery set. A genuinely
   unscoped run keeps the nil default, which go-mutants interprets as `./...`.
2. A changed `_test.go` selects its whole package as a mutation boundary. An
   exact production-file change remains exact unless a changed test file has
   already widened that package. Package patterns are normalized, sorted, and
   deduplicated before preparation.
3. Workspace inspection runs one `go list -json` for the user's selected
   patterns and build tags. Version and module inspection remain separate. The
   broader first listing that was discarded before the selected listing is
   removed.
4. The external cache program may consult cmd/go's host native cache only after
   both owned layers miss. It reads only the requested action index, validates
   the native v1 record, action ID, output ID, declared size, regular-file
   identity, and SHA-256 content, then copies the object into owned storage and
   atomically publishes an owned action. It never returns the host path.
5. An explicit `GOCACHE` uses its last case-insensitive environment declaration
   when it is absolute. `GOCACHE=off` and relative values disable bootstrap. In
   the absence of an explicit value, goatest uses `go-build` below the user cache
   directory. Symlinks are resolved before use, and a source equal to the
   scratch or persistent destination is refused.
6. Missing, malformed, irregular, changing, or content-mismatched native entries
   are cache misses. Source resolution and import failure never change the
   command, evidence, report, or verdict.
7. The frozen mutation workspace excludes `reports` and `dist` in addition to
   `.goatest`. These trees are generated outputs outside the assurance input
   digest. The same boundary is used by verification, planning, fallback
   controls, and repair validation.

## Soundness

Discovery scope changes only which source packages may produce candidates. The
same explicit or changeset boundary already defines that set; downstream test
packages remain available for baseline execution and mutant verification but
cannot add out-of-bound candidates. The nil case retains repository-wide
discovery. Go-mutants still loads, type-checks, validates, and instruments every
selected candidate package under the frozen snapshot and environment.

The single package listing is the exact listing from which the previous
implementation built its model. Removing an earlier unused `./...` listing
cannot remove model input. Test imports and transitive package closures are
still derived from that selected result.

A host-cache hit is only a compilation optimization. The action name must equal
the requested cmd/go key, the output name must be a valid content identifier,
the size must agree, and hashing the complete regular file must reproduce that
identifier. The verified bytes are copied before an owned action becomes
visible. A concurrent host-cache replacement can therefore produce either a
verified immutable copy or a miss, never unverified bytes in a live command.
The compiler remains the fallback authority for every miss.

None of these cache facts participate in behaviour keys or assurance evidence.
They can change process counts and elapsed time only.

Snapshot exclusions do change the bytes available to commands, so only trees
already outside the assurance input boundary are excluded. A baseline that
depends on one fails instead of being measured against a partial repository.
Keeping generated outputs in the copied workspace would be worse: the command
could observe bytes that cannot invalidate the snapshot identity.

## Measured validation

For an isolated empty goatest cache verifying `internal/testargs`, host-native
miss import reduced actual compiler executions in mutation preparation from 381
to 154. The trace completed in 25.959 seconds, with 543 verified native hits and
101 remaining misses.

After package-scoped discovery and validation, the same empty-cache package
completed in 9.93 seconds: snapshot took 1.819 seconds, baseline 4.712 seconds,
mutation preparation 3.122 seconds, probe 8 milliseconds, mutation execution
18 milliseconds, and finalization 54 milliseconds. A process trace confirmed
that the former preparation compiles occurred inside go-mutants'
repository-wide discovery workspace. Workspace inspection then fell from two
package listings to one; an explicit package run records no `go list ./...`
command.

One later run under severe unrelated I/O contention copied 37 MiB of generated
`reports` and `dist` output and spent 96.390 seconds in snapshot creation while
using only 8.01 CPU-seconds overall. After excluding those output trees, the
same two-mutant package under low pressure spent 1.054 seconds in snapshot
creation and 3.88 seconds end to end. The different pressure means the elapsed
ratio is not attributed wholly to the code change; removing 37 MiB from every
snapshot is the deterministic comparison.

A 199-mutant package then completed in 6.27 seconds, and replaying one survivor
from its report completed in 4.85 seconds. The replay was invoked without test
arguments; its trace retained the report's `-test.short=true` and
`-test.parallel=3` arguments together with its package, contract, jobs, and
timeouts.

Wall times are supporting observations because other CPU-heavy work shared the
host. Compiler executions, requested package patterns, cache validation counts,
and traced command classes are the stable comparisons.

## Consequences

- A clean machine can reuse exact local compiler work without pre-seeding
  goatest's persistent cache or scanning the host cache.
- A scoped run pays discovery and type-checking for its candidate packages, not
  for unrelated repository packages.
- Warm runs retain the owned persistent cache path; host bootstrap naturally
  disappears as owned hits replace misses.
- Corrupt or incompatible host entries cost a normal compile and require no
  repair or user choice.
- Native-hit counts and bytes appear in `build-cache-summary`, so cold-path
  regressions remain traceable.
