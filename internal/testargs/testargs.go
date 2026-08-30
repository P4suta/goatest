// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testargs validates and canonicalizes arguments passed directly to a
// compiled Go test binary.
package testargs

import (
	"fmt"
	"slices"
	"strings"
)

// Normalize converts go test's -short shorthand to the equivalent test-binary
// flag and rejects standard test flags whose ownership is required for
// assurance routing. Short mode and the parallel worker limit are the only
// standard test-binary settings exposed as user execution conditions.
func Normalize(arguments []string) ([]string, error) {
	result := slices.Clone(arguments)
	for index, argument := range result {
		switch {
		case argument == "-short" || argument == "--short" || argument == "-test.short" || argument == "--test.short":
			result[index] = "-test.short=true"
			continue
		case strings.HasPrefix(argument, "-short="):
			result[index] = "-test.short=" + strings.TrimPrefix(argument, "-short=")
			continue
		case strings.HasPrefix(argument, "--short="):
			result[index] = "-test.short=" + strings.TrimPrefix(argument, "--short=")
			continue
		case strings.HasPrefix(argument, "-test.short="):
			continue
		case strings.HasPrefix(argument, "--test.short="):
			result[index] = "-test.short=" + strings.TrimPrefix(argument, "--test.short=")
			continue
		case argument == "--test.parallel" || strings.HasPrefix(argument, "--test.parallel="):
			result[index] = "-" + strings.TrimPrefix(argument, "--")
			continue
		case argument == "-test.parallel" || strings.HasPrefix(argument, "-test.parallel="):
			continue
		case strings.HasPrefix(argument, "-test.") || strings.HasPrefix(argument, "--test."):
			return nil, fmt.Errorf("goatest: test-binary argument %q conflicts with an assurance-owned flag", argument)
		}
	}
	return result, nil
}
