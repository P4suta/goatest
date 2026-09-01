// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package devgates holds the repository-wide developer gates. It carries no
// production code: every rule it keeps lives in a test beside this file, and
// the ledger those tests read lives beside them.
//
// The rules are structural rather than behavioural. They answer a question the
// compiler cannot — does a package still replace a package-level variable to
// test itself? — and they answer it from the source tree alone, so a developer
// and CI reach the same verdict.
package devgates
