# goatest

`goatest` is an audit-oriented assurance runner for Go 1.26 and newer. It
connects native Go tests and fuzz targets with coverage routing, mutation
testing, targeted native fuzzing, race checks, explicit integration resources,
and reviewable repair candidates.

The current release line is a pre-release alpha; `v1` is the first intended
public contract and is defined directly, without a legacy compatibility layer.

The goal is narrower than proving program correctness. `ASSURED` means that a
recorded full-project scope completed its configured fault model without
missing evidence. A changeset or package run receives a scope-specific verdict
and can never overwrite the latest full-project result.

## What a verification does

For `standard-v1`, goatest:

1. freezes an exact input identity covering source, tests, corpus, dependencies,
   toolchain, platform, declared environment, configuration, and tool versions;
2. classifies native `TestX`, `FuzzX`, and `ExampleX` results through
   `test2json`, including skips and setup failures;
3. routes tests by coverage blocks and mutant spans;
4. runs relevant race checks (reported as a static estimate in
   `standard-v1`; `deep-v1` races every package);
5. evaluates every selected `go-mutants` mutant, with a passing original
   control immediately before a repeated kill confirmation;
6. records survivors, inconclusive/flaky outcomes, compile rejections,
   acceptances, and out-of-scope mutants as a complete ID-level inventory; and
7. stores any killing corpus or generated test as a candidate. `verify` never
   changes source or corpus.

`deep-v1` expands operators and exploration limits and requires race execution
for every resolved package.

## Install

```console
go install github.com/P4suta/goatest/cmd/goatest@latest
```

Prebuilt archives for Linux, macOS, and Windows (amd64/arm64) are on the
[releases page](https://github.com/P4suta/goatest/releases), each with a syft
SBOM and a GitHub build-provenance attestation. Verify the one archive you
downloaded, for example:

```console
gh attestation verify goatest_0.1.0_Linux_x86_64.tar.gz --repo P4suta/goatest
```

Building from a checkout works the same way: `go build -o goatest ./cmd/goatest`.

## Try it

```console
goatest init
goatest doctor
goatest plan ./...
goatest verify ./...
goatest verify --changed=origin/main ./... -- -short
goatest verify --contract=deep-v1 ./...
```

`goatest init` writes an
annotated `.goatest.toml` and suggests the next steps, including adding
`.goatest/` and `reports/` - the directories every verification writes - to
`.gitignore`. A bare `goatest` prints the help text; `goatest help COMMAND` or
`goatest COMMAND --help` explains one command.

The command surface is:

```text
goatest verify [packages...] [flags] [-- test-binary-args...]
goatest plan [packages...]
goatest doctor
goatest init
goatest explain ID
goatest replay ID
goatest accept ID --reason=TEXT --expires=RFC3339 [--owner=NAME] [--ticket=ID]
goatest fix [ID...] [--apply]
goatest report [--latest-full|--run=ID]
goatest cache status|gc
goatest trace summary [RUN]
goatest trace diff RUN-A RUN-B
goatest help [command]
```

Every command accepts `--ui=auto|plain|jsonl` and `--json`. `auto` renders a
compact in-place dashboard - current phase, elapsed time, mutant progress, and
an estimated remainder - on an interactive terminal and deterministic plain
lines everywhere else; `plain` always renders the deterministic lines; `jsonl`
streams one JSON progress event per note to stdout and ends with the final
`{"type":"report",...}` event, which is the stream's one stable contract.
`--json` emits the report object. Exit codes are
`0` for an assured/resolved/completed operation, `1` for `DEFECT` or
`REPRODUCED`, `2` for `INSUFFICIENT`, `3` for configuration/tool errors, and
`130`/`143` for interruption/termination.

`verify` and `replay` accept `--trace[=DIR]`, which records what the run did
(the phases it passed through, the commands it ran, and how coverage routed
each mutant) as JSON Lines under `DIR`, or under `.goatest/trace/` by default.
`GOATEST_TRACE=1` or `GOATEST_TRACE=DIR` asks for the same without a flag. A
trace is diagnostic exhaust, never evidence: it takes no part in a verdict or
in the identity a cached result is keyed on, and a trace that cannot be
written costs a warning rather than the run. `goatest trace summary [RUN]`
reports missing streams, sequence gaps, a missing `run-end`, and dropped-event
counts; `goatest trace diff RUN-A RUN-B` compares event counts and phase
durations without replaying either run.

Verification holds an OS advisory lock on `.goatest/cache/` for the whole run.
A second process reports `cache-wait` and waits interruptibly. If a run stops
before producing its durable report, a strict exact-input checkpoint remains
under `.goatest/cache/v1/<digest>/checkpoint-v1.json`; the next identical run
automatically reuses completed baseline targets, the complete race phase, and
terminal mutant results. There is no resume flag. See
[checkpoint v1](docs/checkpoint-v1.md) for invalidation and lifecycle rules.

## Verdicts and report history

- `ASSURED`: the resolved scope is the configured full project.
- `CHANGE_ASSURED`: the requested and resolved changeset scope completed.
- `SCOPE_ASSURED`: an explicit package scope completed.
- `REPRODUCED` / `RESOLVED`: replay operation outcome.
- `DEFECT`: user code failed a baseline, race, build, vet, or test contract.
- `INSUFFICIENT`: execution completed but evidence gaps remain.
- `ERROR`: evidence is incomplete or a tool/provider/filesystem failure stopped
  the run.

Every completed verification writes immutable artifacts to
`reports/runs/<run-id>/`: JSON, self-contained searchable HTML, SARIF, JUnit,
and the report v1 JSON Schema. `reports/latest-any.json` and
`.goatest/latest-any.json` follow every run. `latest-full.json` advances only
for a full run, so a 13-mutant changeset report cannot replace a 2,396-mutant
full report.

See [the assurance contract](docs/assurance-contract.md) and
[report v1](docs/report-v1.md) for the exact invariants.

## Existing tests stay ordinary Go tests

Native `TestX`, `FuzzX`, `ExampleX`, subtests, external test packages, and
custom `TestMain` flags remain standard Go. goatest does not provide an
assertion, mock, property, or container framework.

The optional Go API only attaches resource metadata:

```go
func TestRepository(t *testing.T) {
	goatest.Run(t, goatest.Integration("postgres", "redis"), func(t *goatest.T) {
		// t embeds *testing.T.
	})
}
```

Projects do not have to import this package. The equivalent directive is:

```go
//goatest:resources postgres redis
func TestRepository(t *testing.T) { /* ... */ }
```

Use Go's native `FuzzX` functions or a mature property library such as Rapid;
see [property testing](docs/property-testing.md).

## Strict `.goatest.toml` v1

Configuration is optional. Unknown keys and versions other than `1` fail
closed.

```toml
version = 1
contract = "standard-v1"

[project]
packages = ["./..."]
exclude = ["generated/**"]

[execution]
build_tags = ["integration"]
test_binary_args = ["-short"]
environment = ["FEATURE_MODE"]
timeout = "10m"
jobs = 4

[cache]
max_bytes = 5368709120
ttl = "720h"

[resources.postgres]
command = ["./tools/postgres-provider"]
timeout = "30s"
shared = true
environment = ["POSTGRES_IMAGE"]

[generation]
command = ["./tools/test-generator"]
allowed_paths = ["**/*_test.go", "**/testdata/fuzz/**"]
environment = ["GENERATOR_TOKEN"]

[[acceptance]]
id = "0123456789abcdef"
reason = "reviewed equivalent boundary"
expires = "2026-12-31T00:00:00Z"
owner = "quality-team"
ticket = "QA-123"
```

Environment entries are names, never `KEY=value`. Only explicitly named
variables (plus the minimum process-launch environment) reach resource or
generation providers. Values are not written to reports.

The cache capacity and TTL also bound `.goatest/trace/` and
`.goatest/diagnostics/`, independently. `goatest cache status` reports all
three stores and `goatest cache gc` collects all three while holding the same
repository lock.

See [configuration and protocols](docs/configuration.md) for details.

## Repair boundary

Targeted fuzz corpus and generated tests share one candidate model under
`.goatest/candidates/`. Candidates include snapshot provenance, content,
preimage identity, validation status, and a report diff. Preview with
`goatest fix`; only `goatest fix --apply` may change the worktree. Application
revalidates in an isolated copy, checks every preimage, commits the batch, and
rolls back earlier files if a later write fails. Concurrent edits are
preserved as artifacts under `.goatest/patches/`.

## Current limitations

The implementation deliberately fails closed where support is incomplete:

- a `go.work` containing multiple main modules is rejected rather than partly
  assured;
- symbolic links in the evidence tree are rejected;
- the resource protocol currently supports start/ready/stop and shared or
  exclusive instances, but not health/reset/log-artifact operations; and
- local benchmarks watch critical paths, but they are observations rather than
  a performance contract, and this project does not claim proof or production
  readiness.

The complete list is maintained in [limitations](docs/limitations.md).

## Development

```console
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -bench 'Benchmark(CheckpointIO|Digest|MutationAccounting|ReportGeneration)$' ./internal/cache ./internal/evidence ./internal/assure ./internal/report
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the `mise`-based workflow, pull
request rules, and source conventions.

Architecture, assurance contracts, protocols, limitations, and CI notes live
under [`docs/`](docs/).

Development is test-driven. The suite includes the full weak-test → survivor →
targeted fuzz → corpus promotion → fresh-session kill flow, exact-cache and
impact-graph cases, provider/resource failures and cleanup, deterministic
renderers, and the external `go-mutants` bridge contract at the commit `go.mod` pins.

Licensed under either [MIT](LICENSE-MIT) or
[Apache-2.0](LICENSE-APACHE), at your option.
