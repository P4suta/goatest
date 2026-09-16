<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture decision records

A record here states a decision that is expensive to revisit and cheap to
forget: what was decided, what it was decided against, and what it costs. The
body is not revised once accepted. A decision that supersedes another says so in
its own Status line, and the record it replaces gains one line saying which one
replaced it.

`internal/devgates` pins this index to the directory beside it, in both
directions: a record with no line here fails, and a line here naming a record
the tree no longer holds fails as loudly.

| Record | Decides |
| --- | --- |
| [0001](0001-seam-policy.md) | Test seams are arguments, not package-level variables |
| [0002](0002-trace-is-not-evidence.md) | A trace is not evidence |
| [0003](0003-no-replay-engine.md) | No general record/replay engine |
| [0004](0004-proof-layers-not-budgets.md) | Proof layers, not budgets |
| [0005](0005-build-cache-goatest-owns.md) | A build cache goatest owns, and what may write to it |
| [0006](0006-every-temporary-directory-has-an-owner.md) | Every temporary directory has an owner |
| [0007](0007-survived-evidence-is-universal.md) | Survived evidence is a universal proposition over the reaching set |
| [0008](0008-controls-before-timeouts.md) | Controls before timeouts |
| [0009](0009-parallel-measurement-serial-commit.md) | Parallel measurement, serial commit |
| [0010](0010-whole-suite-reach-before-fallback.md) | Whole-suite reach before fallback execution |
| [0011](0011-append-only-checkpoint-journal.md) | Append-only checkpoint journal |
| [0012](0012-aggregate-proof-before-timeout.md) | Exact compatible-group mutation proofs |
| [0013](0013-preserve-block-routing-across-resume.md) | Preserve block routing across resume |
| [0014](0014-resume-complete-probe-phase.md) | Resume a complete probe phase |
| [0015](0015-execute-framed-baselines-directly.md) | Execute framed baseline targets directly |
| [0016](0016-publish-every-baseline-control.md) | Publish every completed baseline control |
| [0017](0017-project-controls-use-a-native-cache-projection.md) | Project controls use a native cache projection |
| [0018](0018-confirm-comparative-watchdogs.md) | Fail closed at comparative watchdogs |
| [0019](0019-instrument-the-test-binary-import-closure.md) | Instrument the test binary import closure |
| [0020](0020-bootstrap-cold-preparation-with-verified-local-work.md) | Bootstrap cold preparation with verified local work |
| [0021](0021-what-a-ledger-cannot-check.md) | What a ledger cannot check |
