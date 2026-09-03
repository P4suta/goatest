// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package buildcache is the build cache goatest owns for the go commands of a
// run, served to them through the documented GOCACHEPROG protocol.
//
// A go command without GOCACHEPROG writes its compile outputs into the user's
// GOCACHE. That is somebody else's disk: a mutation run compiles the module
// from a snapshot whose absolute path is new every time, and the tests of a
// project that spawn go commands of their own compile fixtures from a new
// t.TempDir() every time, so almost nothing either writes is ever read again.
// Measured on goatest's own suite that is gigabytes an hour, and the go
// command's own trimming only removes what has been unused for five days.
//
// So goatest brings its own, in two layers. The base layer is persistent and
// per machine: the standard library, the external dependencies, and the cover
// mode build of the standard library are compiled once and served to every
// later run on that machine. The scratch layer belongs to one run and is
// removed with it, which is where everything a run produces that nobody will
// ever read again goes.
//
// Reads resolve scratch first and then base. Writes land in scratch, except
// for the commands goatest issues itself to compile and to list, which are
// served by a program started with --persist and write to base. Which commands
// those are is not decided here: this package serves whichever layer the
// command line named it, and internal/assure decides. Nothing here reads or
// writes the user's GOCACHE, and nothing here depends on its on-disk format:
// the layout below is goatest's own.
//
//	<layer>/README                       what this is, for whoever finds it
//	<layer>/objects/<oo>/<output id hex> one output, content addressed
//	<layer>/actions/<aa>/<action id hex> one JSON line naming that output
//	<layer>/stats/<pid>-<nanos>.json     what one go process asked for
//
// An object is shared by every action that produced it, and an action is one
// short line, so an entry costs its output once however many action IDs reach
// it. Every write is a temporary file renamed into place and every removal is
// an unlink, which is what lets any number of go processes, of any number of
// concurrent runs, use one layer without coordinating.
package buildcache
