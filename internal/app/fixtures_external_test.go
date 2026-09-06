// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/report"
)

const appFixtureProcessID = 4242

func appTestDigest(character string) string {
	return strings.Repeat(character, hex.EncodedLen(sha256.Size))
}

func appTestExecution() report.Execution {
	return report.Execution{
		MutationJobs:     1,
		CommandTimeoutNS: int64(time.Minute), TargetTimeoutNS: int64(time.Minute),
	}
}
