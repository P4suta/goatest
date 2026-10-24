# CI usage

A packaged GitHub Action isn't required.
A repository can install a tagged release with `go install github.com/P4suta/goatest/cmd/goatest@latest`, or build the checked-out source and use the command directly:

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

To diagnose a run that only misbehaves on the runner, set `GOATEST_TRACE: '1'` on the verify step and upload `.goatest/trace/` with the reports.
Each run writes its own directory named for its start time and process.
A trace records phases, commands, and mutant routing for diagnosis.
Tracing changes neither the verdict nor the cache identity of the run and supplies no assurance evidence.
See [trace v1](trace-v1.md) for the format and [the trace decision](adr/0002-trace-is-not-evidence.md) for why a failed trace never fails the step.

## This repository

Pull requests and merge groups run formatting, vet, package tests, and lint on Linux.
The `required` job rejects failed or cancelled dependencies.
CI cancels superseded runs of the same ref, and merging into `main` doesn't repeat the checks.

The tag release workflow runs the full platform matrix, race detection, lint, and snapshot packaging.
These checks use read-only permissions and must pass before publication starts.
The publishing job holds the release and attestation permissions.

Run `mise run check` for the local fast gate, `mise run test-race` for race detection, and `mise run package` for snapshot packaging.
See [development](development.md) for the tests and benchmarks.
