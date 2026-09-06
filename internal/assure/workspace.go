// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

const (
	internalOutputDirectory     = ".goatest"
	reportOutputDirectory       = "reports"
	distributionOutputDirectory = "dist"
)

func assuranceSnapshotExclusions() []string {
	return []string{reportOutputDirectory, distributionOutputDirectory}
}
