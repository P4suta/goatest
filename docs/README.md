<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Documentation

`internal/devgates` pins this index to the directory, in both directions: a page
with no line here fails, and a line here naming a page the tree no longer holds
fails as loudly. A page nothing checks is a page that will be wrong, and the only
question is when.

## What it does, and will not do

| Page | Subject |
| --- | --- |
| [architecture.md](architecture.md) | the shape of a run, and which package owns which part of it |
| [limitations.md](limitations.md) | what goatest refuses to do, and what is deferred rather than broken |

## Contracts

These are the documents something else validates against. Each is pinned to the
code by a ledger in the package that owns it.

| Page | Subject |
| --- | --- |
| [assurance-contract.md](assurance-contract.md) | what `ASSURED` means, the fault model, and the mutant accounting |
| [report-v1.md](report-v1.md) | the report a run writes, its artifacts, and the exit codes |
| [trace-v1.md](trace-v1.md) | the recording a run keeps, and every closed vocabulary in it |
| [checkpoint-v1.md](checkpoint-v1.md) | how an interrupted run is resumed, and when its evidence is refused |
| [protocols.md](protocols.md) | the resource and generation provider protocols |

## Surfaces

| Page | Subject |
| --- | --- |
| [configuration.md](configuration.md) | every `.goatest.toml` section and key |
| [property-testing.md](property-testing.md) | native fuzzing, and what goatest deliberately does not define |

## Working on it

| Page | Subject |
| --- | --- |
| [development.md](development.md) | the test harness, the two tiers, tracing, keep-temp, and the seam policy |
| [ci.md](ci.md) | using goatest in a workflow, and this repository's own checks |
| [adr/](adr/README.md) | decisions that are expensive to revisit and cheap to forget |
