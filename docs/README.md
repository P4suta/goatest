# Goatest guide

goatest is an audit-oriented assurance runner that connects Go tests and fuzz targets with mutation testing and durable evidence.

The current release line is a pre-release alpha, and `v1` defines the first intended public contract.

## Scope and assurance

`ASSURED` means that a recorded full-project scope completed its configured fault model without missing evidence.

Changeset and package runs receive separate verdicts, so they can't overwrite the full-project result.

goatest provides evidence about its configured checks, not proof that a program is correct or ready for production.

The `standard-v1` contract reports race coverage as a static estimate, while `deep-v1` runs race checks for every resolved package.

## Install from source

The Go module declares Go 1.26, and `mise.toml` pins Go 1.27.3 for source development.

Clone the source and install the pinned Go tool before building the command from that checkout.

```sh
git clone https://github.com/P4suta/goatest.git
cd goatest
mise install go
mise x -- go install ./cmd/goatest
```

## Initialize and plan

`goatest init` writes a minimal `.goatest.toml` and suggests adding `.goatest/` and `reports/` to `.gitignore`.

`goatest plan ./...` previews the resolved package scope and planned checks.

Download the project's modules first because planning resolves dependency metadata offline.

```sh
mise x -- go mod download all
goatest init
goatest plan ./...
```

## Verify, diagnose, and review

`goatest verify ./...` freezes the inputs, runs the configured checks, and records a scope-specific verdict.

Verification uses native tests and fuzz seeds, coverage routing, mutation checks, and configured resource providers.

`verify` leaves source and test corpora unchanged, and it stores generated repairs as reviewable candidates.

`goatest doctor` checks the local tool and repository setup before a run.

Use `goatest explain ID` to inspect a recorded result and `goatest replay ID` to reproduce its target.

The optional `--trace` output records phases and routing for diagnosis, but traces don't affect verdicts or provide evidence.

`goatest trace summary` reports missing events and sequence gaps, while `goatest trace diff RUN-A RUN-B` compares runs.

## Durable reports

Every completed verification writes immutable results under `reports/runs/<run-id>/`.

Each run contains a `JSON` report, searchable `HTML`, `SARIF` findings, `JUnit` results, and the report schema.

`reports/latest-full.json` advances only after a full-project run.

`goatest report --latest-full` reads that durable full-project result.

## Current limits

goatest rejects a `go.work` file that contains more than one main module and rejects symbolic links in the evidence tree.

The resource protocol supports start, ready, and stop operations with shared or exclusive instances.

It doesn't support resource health checks, reset operations, or log artifacts.

Local benchmarks observe critical paths, but they don't define a performance contract.

See the [limitations guide](limitations.md) for the full list.

## More documentation

- [Architecture](architecture.md)
- [Assurance contract](assurance-contract.md)
- [Configuration](configuration.md)
- [Checkpoint v1](checkpoint-v1.md)
- [Protocols](protocols.md)
- [Report v1](report-v1.md)
- [Trace v1](trace-v1.md)
- [Limitations](limitations.md)
- [Development](development.md)
- [Continuous integration](ci.md)

[Full original README at commit 244027f](https://github.com/P4suta/goatest/blob/244027fa877e72e5eb37b79af97ee8e01e7d6069/README.md).
