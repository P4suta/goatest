# Configuration v1

`.goatest.toml` is optional and strict. Missing configuration uses
`standard-v1`, `./...`, a ten-minute execution timeout, and a cache capped at
5 GiB and 30 days. Unknown keys, malformed values, and any `version` other than
`1` are errors.

`goatest init` writes an annotated skeleton: the two active defaults, and every
section below as commented guidance, so turning a setting on is uncommenting a
line. Loading the untouched skeleton yields exactly the defaults.

## Sections

- `[project]`: `packages` and explicit `exclude` patterns. Excludes are project
  boundaries and appear as report limitations.
- `[execution]`: `build_tags`, `test_binary_args`, environment-name allowlist,
  positive `timeout`, and non-negative `jobs`.
- `[cache]`: non-negative `max_bytes` and positive `ttl`. The same policy is
  applied independently to exact-input cache entries, trace run directories,
  and failure-diagnostics directories. Checkpoints live inside their
  exact-input cache entry and are collected with it.
- `[resources.<capability>]`: provider `command`, positive `timeout`, one of
  `shared` or `exclusive`, and environment-name allowlist.
- `[generation]`: provider `command`, `allowed_paths`, and environment-name
  allowlist.
- `[[acceptance]]`: `id`, `reason`, RFC3339 `expires`, and optional `owner` and
  `ticket`. IDs must be unique and fields cannot contain surrounding
  whitespace.

CLI package patterns and test-binary arguments override configured defaults for
that invocation. Build tags, packages, `-short` or custom TestMain flags,
timeouts, and job limits propagate through package inspection, baseline,
coverage, mutation verification, race, and candidate validation.

goatest owns the standard `-test.*` flags that can alter routing, repetition,
selection, output protocols, or completeness. Passing them after `--` is
rejected because it could invalidate assurance evidence. `-short` (normalized
to `-test.short=true`) and `-test.parallel` are the supported standard
test-binary execution settings; custom TestMain flags remain available.

## Environment and secrets

Configuration contains environment variable names, never values. Build-affecting
Go/CGO variables and `[execution].environment` participate in exact cache
identity. Resource and generation subprocesses receive only minimum launch
variables plus the names explicitly allowed for that provider. Values are not
written to reports, diagnostics, or candidate provenance.

Provider binaries are trusted local code with the permissions of the goatest
process. All protocol input/output is size-bounded and strictly decoded, but
process isolation is not a security sandbox. Keep provider commands under the
same review and supply-chain policy as build tooling.

Protocol details are in [protocols](protocols.md).

## Cache maintenance

`goatest cache status` reports the exact-input cache, `.goatest/trace`, and
`.goatest/diagnostics`. `goatest cache gc` removes expired entries first and
then the oldest entries until each store meets `max_bytes`. Verification and
maintenance hold one OS advisory lock rooted at `.goatest/cache`; a contending
process reports `cache-wait`, and interrupting that wait does not start work or
run GC without the lock.
