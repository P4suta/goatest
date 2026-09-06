// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import "time"

const (
	threeEventTraceCount      = 3
	twoEventTraceCount        = 2
	nestedPhaseTraceCount     = 5
	phaseFixtureDuration      = 1500 * time.Millisecond
	prepareFixtureDuration    = 875 * time.Millisecond
	invalidDurationMS         = -1
	innerPhaseDurationMS      = 1000
	outerPhaseDurationMS      = 3000
	outputOverflowBytes       = 4096
	memoryFixtureEventCount   = 5
	memoryFixtureDroppedCount = 3
	memorySinkFixtureCapacity = 2
	snapshotMutationSequence  = 99
	snapshotMutationExitCode  = 137
	teeFixtureEventCount      = 4
	firstTeeDroppedCount      = 4
	secondTeeDroppedCount     = 3
	totalTeeDroppedCount      = 7
	recorderDroppedCount      = 2
)
