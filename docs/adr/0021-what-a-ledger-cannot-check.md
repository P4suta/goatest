---
date: 2026-09-07
status: proposed
---

<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Check the scope and use of verification inventories

## Context

Agreement between two enumerations can miss a wrong scope, an unused gate, or an unsupported explanation.
The trace vocabulary and test tiers exposed those gaps.

## Decision

Keep one authoritative vocabulary and derive consumers where possible.
Compare documentation and executable inventories in both directions.
Check that workflows invoke the declared gates.
Test claims about current behavior through the actual mechanism.
Identify assumptions and preserve the scope, environment, and mechanism behind measurements.

## Alternatives

Manual comparison leaves duplicate definitions free to drift.
Set equality alone cannot establish that the selected set covers the intended behavior.
A borrowed measurement does not establish local runtime cost.

## Consequences

Mechanical agreement complements review of scope and reasoning.
A passing inventory does not prove correctness.
Reconsider when an executable semantic check can replace a remaining assumption.

## Confirmation

Run the documentation, workflow, tier, and rejecting fixtures.
Require the interruption regressions to preserve diagnostics.
Verification and owner acceptance remain pending.
