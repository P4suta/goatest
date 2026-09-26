# Contributing

## Setup

Tool versions are pinned in `mise.toml`. Install [mise](https://mise.jdx.dev)
and run:

```console
mise install
mise run check
```

`mise run check` runs the local gates: formatting (`gofmt`, `taplo`),
build and vet, the full test suite, and lint (`golangci-lint`, `actionlint`,
`typos`, `gitleaks`). CI runs the same gates plus what a single machine
cannot: the three-OS test matrix, the race job, and the goreleaser snapshot
packaging. Individual tasks are listed by `mise tasks`.

The test suite exercises the real Go toolchain and takes a few minutes. Use
`mise run test-race` before opening a pull request that touches concurrency,
and `mise run package` to reproduce the packaging job locally.

## Pull requests

- Open a pull request against `main`; direct pushes are rejected.
- Every CI job must pass before merging, including the macOS and Windows test
  matrix and the lint job. Paths returned by `t.TempDir()` may pass through
  symbolic links or short names on those runners, so resolve them before
  comparing against canonicalized paths.
- Pull requests are squash-merged, so the PR title becomes the commit message
  and must follow Conventional Commits: `feat:`, `fix:`, `perf:`, `test:`,
  `docs:`, `chore:`, `ci:`. The release changelog excludes `docs:`, `test:`,
  `chore:`, and `build:` entries.
- Behavior changes need tests. Development is test-driven; see the
  `Development` section of the README for what the suite covers, and
  [docs/development.md](docs/development.md) for the test harness and workflow.

## Source conventions

- Every `.go`, `.yml`, and `.toml` file starts with the SPDX header used
  across the repository:

  ```go
  // SPDX-FileCopyrightText: 2026 goatest contributors
  // SPDX-License-Identifier: MIT OR Apache-2.0
  ```

- Files are LF-terminated (`.gitattributes`); `gofmt` output is authoritative.

## Dependencies

- Mend-hosted Renovate proposes weekly updates.
  It manages Go modules, GitHub Actions, and tool versions in `mise.toml`.
  Actions use commit SHA pins with version comments.
  Keep that format when editing workflows.
- Go and GoReleaser versions also appear in `.github/workflows`.
  Keep those copies aligned with `mise.toml` when editing them manually.

## Releases

Tags matching `v*` trigger the release workflow, which builds cross-platform
archives with goreleaser and publishes them to GitHub Releases. Release tags
cannot be deleted or moved once pushed.

## License

goatest is dual-licensed under [MIT](LICENSE-MIT) or
[Apache-2.0](LICENSE-APACHE). By contributing you agree that your
contributions are licensed under the same terms.
