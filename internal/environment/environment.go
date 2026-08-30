// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package environment builds deterministic, explicit subprocess environments.
package environment

import (
	"os"
	"slices"
	"strings"
)

var providerLaunchNames = []string{
	"COMSPEC", "HOME", "PATH", "PATHEXT", "SYSTEMDRIVE", "SYSTEMROOT",
	"TEMP", "TMP", "TMPDIR", "USERPROFILE", "WINDIR",
}

// Provider returns only the minimal process-launch environment plus explicitly
// allowed names. A nil input reads the current process environment; an empty
// non-nil input remains empty.
func Provider(input, allowed []string) []string {
	names := append(slices.Clone(providerLaunchNames), allowed...)
	return Select(input, names)
}

// Select returns case-insensitively selected KEY=value entries in stable order.
func Select(input, allowed []string) []string {
	if input == nil {
		input = os.Environ()
	}
	selected := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		selected[strings.ToUpper(name)] = struct{}{}
	}
	values := make(map[string]string, len(selected))
	names := make(map[string]string, len(selected))
	for _, entry := range input {
		key, value, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if !ok || key == "" {
			continue
		}
		if _, ok := selected[upper]; !ok {
			continue
		}
		values[upper] = value
		names[upper] = key
	}
	result := make([]string, 0, len(values))
	for upper, value := range values {
		result = append(result, names[upper]+"="+value)
	}
	slices.Sort(result)
	return result
}
