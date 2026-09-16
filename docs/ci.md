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
| `dogfood` | goatest verifying what the branch changed, with itself |
| `package` | cross-platform snapshot archives |

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

The `dogfood` job uses the changeset scope, which is the scope this page
recommends above and the only one that fits a check answering a pull request. A
full run of this repository against itself takes over an hour - the mutation
phase evaluates every mutant of 2591 tests - and `ASSURED` is defined over a full
scope, so `mise run dogfood` is the one whose verdict means something and
`mise run dogfood-changed` is the one CI can wait for. The first version of this
job ran the full scope, which was wired in before it had been timed.

Packaging, signing, and publishing a dedicated Action are outside the current
self-application roadmap.
