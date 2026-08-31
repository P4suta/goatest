// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package trace records what a run did as a deterministic stream of JSON Lines
// events: the phases it entered, the commands it executed, the mutants it
// routed and ran, and the artifacts it wrote.
//
// A trace is diagnostic exhaust, never evidence. Assurance claims are
// fail-closed; a trace is not. Recording is best effort, and a sink that cannot
// keep an event drops it instead of failing the run. What the package refuses
// to do is lie about the loss: every sink counts its drops and every recording
// ends with a run-end event that reports how many events were emitted and how
// many were dropped.
//
// A trace is also secret safe. An exec record names the environment variables a
// command saw and never their values, and captured command output is digested
// into the event and preserved beside the trace rather than inside it.
//
// The zero cost of a disabled trace is a nil *Recorder: every method is nil
// receiver safe, so callers record unconditionally and never branch on whether
// tracing is on.
package trace
