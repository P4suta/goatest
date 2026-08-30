# Provider protocols v1

Both protocols use newline-delimited, strict JSON and reject unknown fields.
They are local subprocess contracts; core performs no network calls.

## Resource provider

One long-lived process reads a start request:

```json
{"version":1,"action":"start","capability":"postgres","request_id":"resource-000001"}
```

It replies on one line:

```json
{"version":1,"status":"ready","instance":"pg-1","environment":{"DATABASE_URL":"postgres://127.0.0.1/test"}}
```

On lease release goatest sends:

```json
{"version":1,"action":"stop","capability":"postgres","request_id":"resource-000001","instance":"pg-1"}
```

The final response must be
`{"version":1,"status":"stopped","instance":"pg-1"}` and the process must
exit. Startup and shutdown are timeout- and process-tree-bounded. Returned
environment cannot override Go/toolchain, temporary-directory, `GOATEST_*`, or
`GO_MUTANTS_*` variables. Multiple capabilities are acquired in stable order;
conflicting returned values are errors.

`shared=true` reuses one live instance while leases exist. `exclusive=true`
serializes the capability and constrains mutation jobs. Health, reset,
per-target lifecycle, and log-artifact messages are not implemented yet.

## Generation provider

Generation is one process per finding. stdin receives:

```json
{
  "version": 1,
  "finding": {
    "id": "0123456789abcdef",
    "kind": "surviving-mutant",
    "path": "pkg/value.go",
    "line": 42,
    "summary": "all reaching tests passed with this mutation active",
    "replay": "goatest replay 0123456789abcdef",
    "mutant": "lt-to-le: < -> <=",
    "mutant_id": "catalog-id"
  },
  "allowed_paths": ["**/*_test.go", "**/testdata/fuzz/**"],
  "snapshot": "sha256-identity"
}
```

stdout returns one strict object:

```json
{
  "version": 1,
  "finding_id": "0123456789abcdef",
  "candidates": [{
    "kind": "patch",
    "path": "pkg/value_test.go",
    "preimage_sha256": "lowercase-sha256",
    "content": "YmFzZTY0LWVuY29kZWQgZmlsZSBieXRlcw=="
  }]
}
```

Candidate kinds are `patch` and `corpus`; Go JSON encodes `content` bytes as
base64. At most 64 candidates and 4 MiB of provider output are accepted.
Allowed paths are still independently confined to `_test.go` or standard
`testdata/fuzz` locations.

Provider output never changes the worktree. Each candidate must pass three
original-code stability runs, two mutant-kill checks, the related suite/race
validation, and preimage checks before it is stored. Explicit `fix --apply`
repeats fresh validation and preimage checks.
