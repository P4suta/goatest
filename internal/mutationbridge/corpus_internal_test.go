// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutationbridge

import "testing"

func TestCorpusPathRejectsCrossPlatformInvalidNames(t *testing.T) {
	for _, path := range []string{
		"./A:/testdata/fuzz/FuzzX/x",
		"testdata/fuzz/FuzzX/\x00",
	} {
		if normalized, ok := corpusPath(path); ok {
			t.Errorf("corpusPath(%q) = %q, true", path, normalized)
		}
	}
}
