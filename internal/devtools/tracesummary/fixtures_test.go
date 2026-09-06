// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	sampleTraceEventCount      = 30
	sampleTraceExecCount       = 6
	sampleProbeInfectionCount  = 2
	incompleteTraceEventCount  = 5
	prepareTraceEventCount     = 8
	longTraceTargetCount       = 4096
	longTraceNameVariantCount  = 17
	summaryOverflowItemCount   = 2
	mutationControlDurationMS  = 2500
	timedOutControlDurationMS  = 1000
	routePayloadCount          = 6
	routeMutantCount           = 5
	coverageReachingRouteCount = 5
	blockRouteCount            = 4
	fileRouteCount             = 2
	routeReachingTargetCount   = 20
	routeFileCandidateCount    = 21
	branchDischargeCount       = 2
	dischargedRouteCount       = 2
	dischargedReachingCount    = 6
	phaseTotalCount            = 2
	tiedDispositionCount       = 3
)

func traceTestDigest(character string) string {
	return strings.Repeat(character, hex.EncodedLen(sha256.Size))
}
