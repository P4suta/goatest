// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package advisorylock takes the exclusive advisory lock an open file carries.
//
// On unix the lock is advisory: it excludes whoever else asks for it through
// flock and nobody else, so it coordinates goatest's own processes rather than
// protecting a file from the world. On Windows the lock is mandatory for the
// one byte it covers, but every file locked here is empty and nothing reads or
// writes that byte, so the effect is the same: only another Try is ever
// refused. It belongs to the open file
// description and not to the process, which means two descriptions of one path
// contend even inside a single process — that is what a cacheprog child and its
// parent are, and it is also what lets a test hold the lock against itself
// without spawning anything. It belongs to the file and not to the name: a
// rename does not carry it across, so a holder of the old file and whoever
// opens the file that replaced its path hold two different locks and contend
// over nothing. Every party has to open the same file for the lock to mean
// anything.
//
// The lock is released by Release, by closing the file, or by the process
// exiting, and the last of those is what keeps a crashed run from wedging the
// caches goatest shares across a machine.
package advisorylock
