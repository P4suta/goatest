<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# goatest

An audit-oriented assurance runner for Go. It embeds
[go-mutants](https://github.com/P4suta/go-mutants) as a library and adds
coverage routing, deterministic fuzz-seed execution, race checks, explicit
integration resources, and reviewable repair candidates on top of it.

`ASSURED` means a recorded full-project scope completed its configured fault
model without missing evidence. It does not mean the program is correct, and
every word of the difference is in
[the assurance contract](docs/assurance-contract.md).

## Conventions

- **Two tiers, enforced by a scan.** A test that starts a real `go` or `git`
  carries `//go:build integration`. The unit tier finishes in about ten seconds
  and holds 2591 of the 2627 tests. `internal/devgates` refuses a file in the
  wrong tier in both directions, because a stale tag hides a file from
  `go test ./...` entirely - not skipped, not failed, not counted, not compiled.
- **A skip is recorded or it is a failure.**
  `internal/devtools/testaudit/skip_ledger.txt` names every test allowed to step
  aside, and holds eleven - every one a platform that cannot do the thing the
  test is about. `GOATEST_TEST_REQUIRE_TOOLS=1`, which CI sets, turns a missing
  tool from a skip into a failure. On Linux and macOS both tiers run with no skip
  at all. A non-verbose `go test` prints `ok` for a package whose every test was
  skipped, which is how a lost toolchain becomes a green run.
- **Seams are arguments, not package-level variables.**
  [ADR 0001](docs/adr/0001-seam-policy.md). The ledger at
  `internal/devgates/seam_allowlist.txt` may shrink and never grow, and a stale
  entry fails as loudly as a new offender. It holds 69 lines. One seam holds a
  whole package serial: three of them cost `internal/app` 184 seconds, and
  removing them left it at 9.
- **Comments are checked, not banned.** Every exported name in a package on
  `internal/devgates/documented_packages.txt` carries a doc comment, every path
  a comment names exists, and every `[Name]` link resolves. That ledger may grow
  and never shrink: it is the mirror of the seam ledger, where a line is a debt
  paid rather than a debt.
- **Goldens fail closed.** A missing golden is a failure, never a silent first
  recording.
- **A trace is not evidence.** [ADR 0002](docs/adr/0002-trace-is-not-evidence.md).
  Tracing changes no verdict and no cache identity, a sink failure costs a note
  rather than the run, and nothing is dropped in silence.
- **Every workspace command runs with `GOWORK=off`**, set once in
  `internal/mutationbridge`. A run assures the one module it was pointed at.
- **SPDX on every file.** The ones whose format cannot hold a header are
  annotated in `REUSE.toml`, and a gate refuses a file that is in neither - or
  an annotation that covers nothing.
- **Conventional Commits**, written as a declarative sentence about behaviour.

## The documentation ledger

Any page that enumerates a set the code also enumerates is pinned to the code by
a test, in both directions. A page nothing checks is a page that will be wrong,
and the only question is when.

| What is pinned | To what | Where |
| --- | --- | --- |
| `docs/trace-v1.md` vocabularies | `internal/trace/vocabulary.go` and `schema.json` | `internal/trace/docs_test.go` |
| the exit-code table and every verdict | `exitCode` and the `Exit*` constants | `internal/cli/docs_test.go` |
| the command surface in `README.md` | the `Command` constants, `commandHelp`, and the dispatch | `internal/cli/docs_test.go` |
| the mutant accounting equations | the arithmetic `audit.go` runs, evaluated | `internal/report/docs_test.go` |
| the disposition vocabulary | the published JSON Schema | `internal/report/docs_test.go` |
| the limitation codes in `docs/limitations.md` | `LimitationCodes()` | `internal/report/limitations_docs_test.go` |
| the run phases and their order | `runPhaseNames` | `internal/assure/trace_phases_docs_test.go` |
| `docs/README.md` and `docs/adr/README.md` | the directories beside them | `internal/devgates/index_test.go` |
| `REUSE.toml` | every file git holds | `internal/devgates/license_test.go` |
| `.gitleaks.toml` allowlist | what git ignores, and what it tracks | `internal/devgates/gitleaks_test.go` |
| where this repository names itself | `repository_references.txt` | `internal/devgates/repository_references_test.go` |
| exported doc comments, and the paths and links inside them | the tree | `internal/devgates/docs_test.go` |
| which files may start a toolchain | the build tags | `internal/devgates/tiers_test.go` |
| package-level seams | `seam_allowlist.txt` | `internal/devgates/seams_test.go` |
| which tests may skip | `skip_ledger.txt` | `internal/devtools/testaudit` |

Adding a page that enumerates something means adding its ledger in the same
change. Each ledger carries a test proving it can fail, because two agreeing
lists is also what it looks like when one of them was read as empty.

[ADR 0021](docs/adr/0021-what-a-ledger-cannot-check.md) records what this
discipline does not cover: a ledger checks that a claim matches a set, and
nothing mechanical checks that the set covers what happens, or that the reason
written beside a choice is correct.

## Where things are

| | |
| --- | --- |
| What it does, and will not do | [docs/architecture.md](docs/architecture.md), [docs/limitations.md](docs/limitations.md) |
| Contracts | [docs/assurance-contract.md](docs/assurance-contract.md), [docs/report-v1.md](docs/report-v1.md), [docs/trace-v1.md](docs/trace-v1.md), [docs/checkpoint-v1.md](docs/checkpoint-v1.md) |
| Surfaces | [README.md](README.md), [docs/configuration.md](docs/configuration.md), [docs/protocols.md](docs/protocols.md) |
| Decisions | [docs/adr/](docs/adr/README.md) |
| Working on it | [CONTRIBUTING.md](CONTRIBUTING.md), [docs/development.md](docs/development.md), [docs/ci.md](docs/ci.md) |
