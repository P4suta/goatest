# CI usage before a packaged Action exists

No GitHub Action or tagged release is published yet. A repository can build the
checked-out source and use the CLI directly:

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
      - name: Build unreleased goatest
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
the cache identity of the run. See
[development](development.md#execution-tracing) for the format.

For this repository itself, the minimum pre-merge checks are `go test ./...`,
`go test -race ./...`, and `go vet ./...`. A release workflow still needs a
three-OS matrix, supported architectures/CGO fixtures, report schema checks,
signed checksums, SBOM, and provenance verification before the first beta.
