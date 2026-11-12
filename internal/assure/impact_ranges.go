// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	gomutants "github.com/P4suta/go-mutants"
)

// hunkHeader matches the new-side span of a unified diff hunk: the `+13,4` of
// `@@ -12,0 +13,4 @@`.
//
// The count is optional, and its absence means one line, which is what a diff
// writes for a single-line change.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// diffPathPrefix opens the line naming the file a hunk belongs to.
const diffPathPrefix = "+++ b/"

// changedLineRanges reports which lines of which changed files are new.
//
// Without it, a changeset run discovers every mutant in a changed file and then
// executes all of them, including the ones in the four hundred lines nobody
// touched. go-mutants has taken a line-range selection since before the version
// pinned here; this module simply never sent one.
//
// It fails closed. A diff it cannot read, a path the caller did not already
// know about, a hunk header it cannot parse - each returns false, and the run
// measures the whole of every changed file exactly as it did before. Narrowing
// on a misread diff would skip a mutant in changed code, which is the one
// mistake a changeset scope may not make.
func changedLineRanges(ctx context.Context, root, reference string, changed []string) (map[string][]gomutants.LineRange, bool) {
	tracked, untracked := splitTrackedChanges(root, changed)
	ranges := make(map[string][]gomutants.LineRange, len(changed))
	for _, path := range untracked {
		lines, ok := fileLineCount(filepath.Join(root, filepath.FromSlash(path)))
		if !ok {
			return nil, false
		}
		if lines == 0 {
			continue
		}
		ranges[path] = []gomutants.LineRange{{First: 1, Last: lines}}
	}
	if len(tracked) == 0 {
		return ranges, true
	}
	base := reference
	if base == "" {
		base = "HEAD"
	}
	arguments := append([]string{
		"-c", "core.quotepath=false", "diff", "--no-ext-diff", "--no-renames",
		"--unified=0", "--diff-filter=ACM", base, "--",
	}, tracked...)
	output, err := gitNamesOutput(ctx, root, arguments)
	if err != nil || ctx.Err() != nil {
		return nil, false
	}
	known := make(map[string]bool, len(tracked))
	for _, path := range tracked {
		known[path] = true
	}
	current := ""
	for _, line := range strings.Split(string(output), "\n") {
		if after, found := strings.CutPrefix(line, diffPathPrefix); found {
			if !known[after] {
				return nil, false
			}
			current = after
			continue
		}
		match := hunkHeader.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if current == "" {
			return nil, false
		}
		span, ok := hunkSpan(match)
		if !ok {
			return nil, false
		}
		if span.Last < span.First {
			continue
		}
		ranges[current] = append(ranges[current], span)
	}
	return ranges, true
}

// hunkSpan turns a parsed hunk header into the lines it added.
func hunkSpan(match []string) (gomutants.LineRange, bool) {
	first, err := strconv.Atoi(match[1])
	if err != nil || first < 1 {
		return gomutants.LineRange{}, false
	}
	count := 1
	if match[2] != "" {
		count, err = strconv.Atoi(match[2])
		if err != nil || count < 0 {
			return gomutants.LineRange{}, false
		}
	}
	if count == 0 {
		// A hunk that only deletes adds no line to select.
		return gomutants.LineRange{First: first, Last: first - 1}, true
	}
	return gomutants.LineRange{First: first, Last: first + count - 1}, true
}

// splitTrackedChanges separates the changed paths git can diff from the ones it
// cannot, which are the untracked files changedFiles collected with ls-files.
func splitTrackedChanges(root string, changed []string) (tracked, untracked []string) {
	for _, path := range changed {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			continue
		}
		tracked = append(tracked, path)
	}
	return tracked, untracked
}

// fileLineCount counts the lines of one file.
func fileLineCount(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if len(data) == 0 {
		return 0, true
	}
	lines := strings.Count(string(data), "\n")
	if !strings.HasSuffix(string(data), "\n") {
		lines++
	}
	return lines, true
}

// mutationSelection narrows the mutants a changeset run executes to the lines it
// changed.
//
// It returns nil - meaning every mutant the include patterns discovered - in
// three cases, and each is a deliberate refusal to narrow rather than an
// oversight.
//
// A broad scope has nothing to narrow against. A run whose diff could not be
// read narrows nothing, because a selection built from a misread diff skips
// mutants in changed code. And a run in which a `_test.go` changed narrows
// nothing at all, not even in the packages whose sources did not: a changed test
// can change the fate of any mutant in its package, so the lines it touched say
// nothing about which mutants it now reaches. That widening is in
// docs/limitations.md and stays there.
func mutationSelection(selection impactSelection) *gomutants.Selection {
	if selection.broad || selection.ranges == nil {
		return nil
	}
	lines := make(map[string][]gomutants.LineRange, len(selection.ranges))
	for _, path := range selection.changed {
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
	}
	for path, spans := range selection.ranges {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		lines[path] = slices.Clone(spans)
	}
	if len(lines) == 0 {
		return nil
	}
	return &gomutants.Selection{Lines: lines}
}
