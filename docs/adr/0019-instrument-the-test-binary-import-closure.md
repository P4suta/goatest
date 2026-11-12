# 0019 — Instrument the test binary import closure

## Status

Accepted, 2026-09-05. Implemented by `internal/assure` and the package model in
`internal/golang`.

## Context

Baseline coverage maps each top-level test to the mutants it can reach. The
previous compile command used `-coverpkg=<module>/...` for every selected test
binary. Even a one-package verification therefore coverage-compiled every
package in the repository. Most of those packages could not be linked into the
test binary and could never contribute a counter.

## Decision

Each baseline test binary is compiled with its own package plus the module
packages in its exact test-binary import closure. The closure comes from
`go list -json`: production dependencies plus the transitive dependencies of
the package's internal and external test imports. External-module packages are
excluded because goatest does not mutate them.

## Soundness

A Go test binary can execute linked Go code only from its own package and its
static import closure. Reflection does not load an unlinked Go package. A child
process, plugin, or independently built executable did not contribute counters
to the parent coverage profile under module-wide `-coverpkg` either, so the
change introduces no new blind spot. The package itself is always included,
and every module dependency named by `go list` is included exactly, with module
path boundaries preventing similarly prefixed modules from entering the set.

The result changes only which unreachable packages the Go compiler is asked to
instrument. Every counter that could map a selected test to an in-process
mutant remains present, so routing and verdict semantics are unchanged.

## Consequences

- Scoped and changed verification no longer coverage-compiles unrelated
  repository packages.
- Full-project verification still covers every reachable module package, but
  each test binary links only its own closure.
- Coverage command lines are deterministic because the exact package set is
  sorted and deduplicated.
