// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import "os"

func HelperArgv(testName string) []string {
	return []string{os.Args[0], "-test.run=^" + testName + "$"}
}

func HelperEnabled(variable string) bool {
	return os.Getenv(variable) == "1"
}
