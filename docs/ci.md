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

For this repository itself, the required checks are `go test ./...`,
`go test -race ./...`, `go vet ./...`, schema tests in those packages, and the
local benchmark set in [development](development.md). Packaging, signing, and
publishing a dedicated Action are outside the current self-application
roadmap.
