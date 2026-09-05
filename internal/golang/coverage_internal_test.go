// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import "testing"

func TestCompareCoverageBlocksUsesStartBeforeEnd(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		first, second CoverageBlock
	}{
		{
			name:   "start line before contradictory ends",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 20, EndColumn: 9},
			second: CoverageBlock{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 1},
		},
		{
			name:   "start column before contradictory ends",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 20, EndColumn: 9},
			second: CoverageBlock{StartLine: 2, StartColumn: 6, EndLine: 4, EndColumn: 1},
		},
		{
			name:   "end line",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9},
			second: CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 5, EndColumn: 1},
		},
		{
			name:   "end column",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 8},
			second: CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareCoverageBlocks(test.first, test.second); got >= 0 {
				t.Fatalf("compareCoverageBlocks(first, second) = %d, want negative", got)
			}
			if got := compareCoverageBlocks(test.second, test.first); got <= 0 {
				t.Fatalf("compareCoverageBlocks(second, first) = %d, want positive", got)
			}
		})
	}

	equal := CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9}
	if got := compareCoverageBlocks(equal, equal); got != 0 {
		t.Errorf("equal coverage blocks compare as %d", got)
	}
}
