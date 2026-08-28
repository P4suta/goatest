# goatest

`goatest` is a mutation-driven assurance runner for Go 1.26 and newer. It asks
a stricter question than “what is the mutation score?”: which changed or
reachable behaviours still lack durable evidence, and can an existing test,
property, fuzz target, integration resource, or generated test close that gap?

The default command verifies the whole module under `standard-v1`. It runs
build and vet checks, compiles one covered test binary per package, maps
top-level test coverage, runs relevant race checks, executes the `strong`
`go-mutants` profile against only proven-reaching targets, and promotes a
mutation-killing fuzz input into Go's standard corpus before starting over from
a fresh snapshot. An unresolved survivor is never converted into success by a
fuzzing timeout or a percentage threshold.

The primary result is one of `ASSURED`, `DEFECT`, `INSUFFICIENT`, or `ERROR`.
No telemetry is emitted. Core execution is offline by default (`GOPROXY=off`,
`GOSUMDB=off`, `GOTELEMETRY=off`, and `GOTOOLCHAIN=local`); configured resource
and generation providers are explicit local processes using versioned JSON.

## Install and run

```console
go install github.com/P4suta/goatest/cmd/goatest@latest
goatest
goatest --changed=origin/main
goatest --contract=deep-v1 --no-apply --json --no-tui
```

Commands:

- `goatest init`
- `goatest explain FINDING_ID`
- `goatest replay FINDING_ID`
- `goatest accept FINDING_ID`
- `goatest report`

Exit codes are stable: `0` assured, `1` reproduced defect, `2` insufficient
evidence, `3` configuration/infrastructure error, `130` interrupt, and `143`
termination. Terminal, pipe, JSON, and CI modes all use the same report verdict.

Every verification writes deterministic artifacts beneath `reports/`:
`assurance-report-v1.json`, a self-contained offline HTML report, SARIF 2.1.0,
JUnit XML, and the JSON Schema. `.goatest/report.json` is the local index for
`explain`, `replay`, and `accept`.

## Existing tests remain ordinary Go tests

No wrapper is required. Existing `TestX` and `FuzzX` targets are discovered and
run unchanged. The small public API is for resource metadata and shared typed
property/fuzz definitions:

```go
func TestRepository(t *testing.T) {
	goatest.Run(t, goatest.Integration("postgres"), func(t *goatest.T) {
		// t embeds *testing.T.
	})
}

func FuzzRoundTrip(f *testing.F) {
	goatest.Check(f, goatest.Unit(), func(t *goatest.T) {
		input := goatest.Draw(t, "input", gen.String())
		_ = input // property under test
	})
}
```

`Check` exposes exactly one `[]byte` to Go's standard fuzz engine. Normal seed
execution, `go test -fuzz`, typed generation, replay tokens, and
mutation-guided fuzzing therefore share the same standard corpus. Package
`gen` includes typed ranges, constraints, combinators, recursive values, state
machines, deterministic shrinking, classification, and versioned replay.

## Optional `.goatest.toml` v1

Unknown keys and unsupported versions are errors.

```toml
version = 1
contract = "standard-v1"

[resources.postgres]
command = ["./tools/postgres-provider"]
timeout = "30s"
shared = true

[generation]
command = ["./tools/test-generator"]
allowed_paths = ["**/*_test.go", "**/testdata/fuzz/**"]

[[acceptance]]
id = "0123456789abcdef"
reason = "reviewed equivalent boundary"
expires = "2026-09-30T00:00:00Z"
```

A resource provider stays alive for `start → ready → stop` messages and returns
test-only environment variables. `goatest` owns sharing, exclusivity,
reference counts, timeouts, signals, and process-tree cleanup. A generation
provider receives one finding, its snapshot identity, and allowed paths; it
returns candidate full-file test patches or standard corpus files. Core has no
LLM SDK or network client.

Generated changes are limited to `_test.go` and `testdata/fuzz/**`. Before an
atomic application, a candidate must pass three original-code stability runs,
kill the target mutant twice, pass the related suite and required race checks,
and still match its preimage hash. Concurrent user edits are preserved and the
candidate is written under `.goatest/patches/` instead.

## Contracts and cache

`standard-v1` requires the baseline, declared resources, relevant race checks,
code reachability, and every selected `strong` mutant to be killed or covered
by an unexpired explicit acceptance. Targeted fuzzing stops after 10,000
executions without a kill; the hard safety ceiling is one million executions
or 30 minutes per target. `deep-v1` uses all operators, races every package,
and expands exploration limits by ten.

Evidence reuse is exact, not heuristic. Source, tests, dependency content,
toolchain, platform, effective environment, resource configuration, corpus,
contract, and both tool versions contribute to the digest. A warm exact run
starts no child test or mutant process. `--changed` reuses a previously assured
top-level coverage/dependency graph; an unknown path or dependency always
broadens execution.

## Development

```console
go test ./...
go test -race ./...
go vet ./...
```

Development is test-driven. The suite includes the full weak-test → survivor →
targeted fuzz → corpus promotion → fresh-session kill flow, exact-cache and
impact-graph cases, provider/resource failures and cleanup, deterministic
renderers, and the external `go-mutants v0.1.0` bridge contract.

Licensed under either [MIT](LICENSE-MIT) or
[Apache-2.0](LICENSE-APACHE), at your option.
