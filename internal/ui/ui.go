// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

type Notes interface {
	Note(kind, detail string)
	Close()
}
