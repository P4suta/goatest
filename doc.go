// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package goatest declares what a Go test needs in order to mean anything.
//
// A test that reaches for a database, a toolchain or a network is not the same
// kind of evidence as one that reaches for nothing, and the difference matters
// twice: a runner has to know what to provide before it starts the test, and a
// reader has to know what the test was allowed to touch before trusting what it
// proved. Go's own vocabulary has no word for either, so a suite ends up saying
// it with a build tag, a skip, an environment variable and a comment — four
// spellings of one fact, none of which a tool can read.
//
// This package is that word. [Run] wraps an ordinary test with a [TestScope]
// declaring whether it is a unit test or an integration test, and in the second
// case naming the capabilities it requires:
//
//	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
//		// nothing outside this process
//	})
//
//	goatest.Run(t, goatest.Integration("postgres", "redis"), func(gt *goatest.T) {
//		// the two capabilities named above, and nothing else
//	})
//
// The declaration is a statement about the test, not a mechanism that enforces
// it. Nothing here starts a database or skips a test, and that is deliberate:
// what to do with an unmet capability is the runner's decision, and a library
// that decided it here would be making it for every runner. The goatest command
// reads these declarations; so may anything else.
//
// A capability is a free-form name. Whether "postgres" means the same thing in
// two suites is a question this package cannot answer and does not try to.
//
// For tests that carry the declaration in a comment rather than in code — a
// fuzz target, or a test a generator writes — the equivalent directive is
// `//goatest:resources postgres redis` on the line above the function.
package goatest
