// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package devgates holds the checks this repository runs against itself.
//
// Nothing here is production code and nothing here is imported by any: the
// package is a doc.go and a set of tests that read the whole tree with go/ast
// and refuse shapes the repository has decided against. A gate lives here
// rather than in the package it polices because what it polices is a property
// of the repository, and a rule that lives inside one package is a rule the
// next package does not inherit.
//
// Each gate carries a ledger, and the direction a ledger may move is the whole
// of its design:
//
//   - seam_allowlist.txt may only shrink. Every line is a package-level
//     variable a test overwrites, which forces that package's tests to run one
//     at a time. A line is a debt.
//   - documented_packages.txt may only grow. Every line is a package whose
//     exported names all carry a doc comment. A line is a debt that has been
//     paid, and the ledger is what keeps it paid.
//
// Both ledgers are checked in both directions. A shape the ledger does not name
// fails as a new offender, and a line the tree no longer earns fails as a
// removal nobody recorded — because a ledger that only notices one of those is
// a ledger that will eventually describe a repository that no longer exists.
package devgates
