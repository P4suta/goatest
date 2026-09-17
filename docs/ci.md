<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# CI usage

A packaged GitHub Action is not required. A repository can install a tagged
release with `go install github.com/P4suta/goatest/cmd/goatest@latest`, or build
the checked-out source and use the CLI directly:

```yaml
name: goatest
on:
  pull_request:
  push:
    branches: [main]

jobs:
  assurance:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.x'
      - name: Build goatest from the checkout
        run: go build -o "$RUNNER_TEMP/goatest" ./cmd/goatest
      - name: Pull-request scope
        if: github.event_name == 'pull_request'
        run: "$RUNNER_TEMP/goatest" verify --changed=origin/${{ github.base_ref }} ./... --ui=plain
      - name: Full main scope
        if: github.event_name != 'pull_request'
        run: "$RUNNER_TEMP/goatest" verify ./... --ui=plain
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: goatest-reports
          path: reports/
```

To diagnose a run that only misbehaves on the runner, set `GOATEST_TRACE: '1'`
on the verify step and upload `.goatest/trace/` with the reports; each run
writes its own directory there, named for its start and its process. A trace
records the phases, commands, and mutant routing of a run; it is diagnostic
exhaust, never evidence, and asking for one changes neither the verdict nor
the cache identity of the run. See [trace v1](trace-v1.md) for the format and
[ADR 0002](adr/0002-trace-is-not-evidence.md) for why a failed trace never
fails the step.

## This repository's own checks

The workflow runs five jobs, and every step that checks something runs a
`mise.toml` task rather than restating it. A workflow that restates a task has
two definitions to keep in step and gives a developer no way to run what CI
runs; `internal/devgates` refuses a job that calls a task mise does not declare,
and one that calls a task without installing mise - which is a failure only the
workflow can find. `internal/devgates` pins this table to the workflow
in both directions, and refuses a job that checks out a shallow clone: without
the whole history, a run asking what changed against a ref it does not hold is
told that nothing did, which is the same answer as a clean tree and a completely
different fact.

| Job | What it runs |
| --- | --- |
| `test` | the unit tier and then the integration tier, on Linux, macOS and Windows, each audited by `internal/devtools/testaudit` |
| `race` | both tiers under the race detector, on Linux |
| `lint` | `golangci-lint`, `actionlint`, `typos`, `gitleaks`, and TOML formatting |
| `dogfood` | goatest verifying what the branch changed, with itself; pull requests only |
| `package` | cross-platform snapshot archives |

`gitleaks` is handed `.gitleaks.toml`, which narrows what it walks. The
scanner does not ask git what it tracks: `gitleaks dir .` reads whatever the
last run left in the working tree, which here was 142 MB against 3.3 MB of
tracked files - the rest being the build cache under `.goatest/` and report
artifacts under `reports/`. That is not only slow, it is a gate that can go red
for content the repository does not contain, which is a failure nobody can fix
by editing the repository and so a failure people learn to scroll past. With
the allowlist the same scan reads 3.37 MB in 62 ms. Each entry is the claim
that a path is outside the repository rather than a judgement that something is
not a secret, and `internal/devgates` checks the claim in the two ways it can be
false: git must ignore the path, and no tracked file may match it. The second is
the one that matters, because a path can be named in `.gitignore` and tracked at
the same time.

The suite is in two tiers. `go test ./...` is the unit tier: everything that
needs nothing but a compiler, which finishes in about ten seconds.
`go test -tags integration ./...` adds the suites that drive a real toolchain -
a `go build`, a git history, a whole mutation run - and takes about half a
minute. `internal/devgates` refuses a test file that starts a toolchain without
the tag, and a file that carries the tag and starts nothing.

Both tiers run with `GOATEST_TEST_REQUIRE_TOOLS=1`, which turns a missing `go`
or `git` from a skip into a failure. Unset it locally: a developer without a
toolchain should see the toolchain tests step aside, while a CI job without one
is a broken job rather than a smaller suite.

The `dogfood` job runs on pull requests and not on pushes to `main`, because the
scope is what the branch changed and on `main` that is nothing: the job would
find no changed file, verify nothing, and report a green check. A check that
cannot fail is worse than an absent one, because it is counted.

The `dogfood` job uses the changeset scope, which is the scope this page
recommends above and the only one that fits a check answering a pull request. A
full run of this repository against itself takes over an hour - the mutation
phase evaluates every mutant of 2591 tests - and `ASSURED` is defined over a full
scope, so `mise run dogfood` is the one whose verdict means something and
`mise run dogfood-changed` is the one CI can wait for. The first version of this
job ran the full scope, which was wired in before it had been timed.

The job's `timeout-minutes` is a bound on a runaway rather than a budget for a
wait, and it is not derived from a measurement of this job. The attempt to take
one is worth recording, because it failed in a way that is easy to repeat: the
scoped run was timed on a developer machine that was concurrently running
another project's verification at a load average of thirteen, and finished at
74% of one core's worth of CPU. That makes the wall clock a measurement of the
machine rather than of the job.

The run's accounting survives the load, and says something the clock would have
hidden. The changeset scope selected 12,090 mutants - which is not what "what
the branch changed" sounds like, and is correct: the branch changed `_test.go`
files, a changed test can alter the fate of any mutant in its package, so the
scope widens to those whole packages and the line ranges that would otherwise
narrow it are not sent. A changeset of production code alone is a far smaller
run than this one. So the largest scoped run this repository has produced is a
branch that rewrote its own test suite, and it is a poor model of the pull
requests this job will actually see.

The bound is therefore set above any run this repository has been seen to take,
against a GitHub default of 360, and that is all it claims.

Reaching it is no longer silent, with one qualification worth stating. A job
that hits its limit is cancelled, its processes are signalled, and a signalled
goatest publishes a report and a diagnostics bundle before it exits - both of
which this job uploads. What it cannot do is publish from a process that is not
running: a run blocked in the kernel, or one still writing when the runner's
grace period ends and the signal becomes a kill, leaves what it had written and
no more. So the bound is generous for that reason too. The evidence a stopped
run leaves is the reason a bound can be generous, and a bound that is generous
is the reason the run gets to leave it.

Packaging, signing, and publishing a dedicated Action are outside the current
self-application roadmap.
