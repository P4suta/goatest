# Contributing

## Setup

`mise.toml` pins the tool versions.
Install [mise](https://mise.jdx.dev) and run:

```console
mise install
mise run check
```

`mise run check` checks formatting, builds and vets the source, runs the test suite, and runs lint.
Pull requests and merge groups run tests and lint on Linux.
The release workflow adds the macOS and Windows tests, race detection, and snapshot packaging before publication.
`mise tasks` lists the individual tasks.

The test suite exercises the real Go toolchain and takes a few minutes.
Use `mise run test-race` when investigating concurrency and `mise run package` to reproduce snapshot packaging.

## Pull requests

- Open a pull request targeting `main`.
  Branch protection rejects direct pushes.
- The `required` check must pass before merging.
  It requires successful Linux tests and lint on pull requests and merge groups.
  Paths returned by `t.TempDir()` may pass through symbolic links or short names, so resolve them before comparing canonical paths.
- Pull requests use squash merges, so the title becomes the commit message and must follow Conventional Commits.
  Use a type such as `feat:`, `fix:`, `perf:`, `test:`, `docs:`, `chore:`, or `ci:`.
  The release changelog excludes `docs:`, `test:`, `chore:`, and `build:` entries.
- Behavior changes need tests.
  Use test-driven development and see the [development guide](docs/development.md) for the test harness and workflow.

## Source conventions

- Every `.go`, `.yml`, and `.toml` file starts with the SPDX header used across the repository:

  ```go
  // SPDX-FileCopyrightText: 2026 goatest contributors
  // SPDX-License-Identifier: MIT OR Apache-2.0
  ```

- `.gitattributes` requires LF line endings.
  `gofmt` defines Go formatting.

## Dependencies

- Mend-hosted Renovate proposes weekly updates.
  It manages Go modules, GitHub Actions, and tool versions in `mise.toml`.
  Actions use commit SHA pins with version comments.
  Keep that format when editing workflows.
- Go and GoReleaser versions also appear in `.github/workflows`.
  Keep those copies aligned with `mise.toml` when editing them manually.

## Releases

Tags matching `v*` trigger the release workflow.
It runs the full checks with read-only permissions before building cross-platform archives and publishing them to GitHub Releases.
Release tags are permanent.

## License

Choose the [Massachusetts Institute of Technology license](LICENSE-MIT) or [Apache License 2.0](LICENSE-APACHE).
By contributing, you license your work under the same terms.
