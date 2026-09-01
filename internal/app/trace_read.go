// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/report"
	"github.com/P4suta/goatest/internal/trace"
)

func (service Service) readTrace(root, action string, runs []string) (report.Report, error) {
	traceRoot := filepath.Join(root, ".goatest", "trace")
	result := report.Report{Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted}
	switch action {
	case "summary":
		name := ""
		if len(runs) != 0 {
			name = runs[0]
		}
		summary, id, err := readNamedTrace(traceRoot, name)
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = traceSummaryEvidence(id, summary)
		return result, nil
	case "diff":
		if len(runs) != 2 {
			return report.Report{}, errors.New("goatest: trace diff requires two runs")
		}
		before, beforeID, err := readNamedTrace(traceRoot, runs[0])
		if err != nil {
			return report.Report{}, err
		}
		after, afterID, err := readNamedTrace(traceRoot, runs[1])
		if err != nil {
			return report.Report{}, err
		}
		result.Evidence = append(result.Evidence, traceSummaryEvidence("before:"+beforeID, before)...)
		result.Evidence = append(result.Evidence, traceSummaryEvidence("after:"+afterID, after)...)
		difference := trace.Diff(before, after)
		status := "changed"
		if difference.EventsDelta == 0 && difference.MissingSequencesDelta == 0 && difference.EventsDroppedDelta == 0 &&
			difference.BeforeVerdict == difference.AfterVerdict && difference.BeforeRunEnd == difference.AfterRunEnd && allZeroCounts(difference.CountDelta) && allZeroDurations(difference.PhaseDurationDeltaMS) {
			status = "unchanged"
		}
		result.Evidence = append(result.Evidence, report.Evidence{
			Kind: "trace-diff", ID: beforeID + ".." + afterID, Status: status,
			Detail: fmt.Sprintf("events=%+d missing-sequences=%+d dropped=%+d run-end=%t->%t verdict=%s->%s",
				difference.EventsDelta, difference.MissingSequencesDelta, difference.EventsDroppedDelta,
				difference.BeforeRunEnd, difference.AfterRunEnd, difference.BeforeVerdict, difference.AfterVerdict),
		})
		keys := make([]string, 0, len(difference.CountDelta))
		for kind := range difference.CountDelta {
			keys = append(keys, kind)
		}
		slices.Sort(keys)
		for _, kind := range keys {
			result.Evidence = append(result.Evidence, report.Evidence{Kind: "trace-diff-count", ID: kind, Status: "compared", Detail: fmt.Sprintf("delta=%+d", difference.CountDelta[kind])})
		}
		phases := make([]string, 0, len(difference.PhaseDurationDeltaMS))
		for phase := range difference.PhaseDurationDeltaMS {
			phases = append(phases, phase)
		}
		slices.Sort(phases)
		for _, phase := range phases {
			result.Evidence = append(result.Evidence, report.Evidence{Kind: "trace-diff-phase", ID: phase, Status: "compared", Detail: fmt.Sprintf("duration-ms-delta=%+d", difference.PhaseDurationDeltaMS[phase])})
		}
		return result, nil
	default:
		return report.Report{}, fmt.Errorf("goatest: trace action %q is unsupported", action)
	}
}

func readNamedTrace(root, name string) (trace.Summary, string, error) {
	if name == "" {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			summary, readErr := trace.ReadSummary(filepath.Join(root, "latest", trace.FileName))
			return summary, "latest", readErr
		}
		if err != nil {
			return trace.Summary{}, "", fmt.Errorf("goatest: read trace root: %w", err)
		}
		var names []string
		for _, entry := range entries {
			if !safeTraceName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return trace.Summary{}, "", fmt.Errorf("goatest: trace entry %q is not a confined directory", entry.Name())
			}
			names = append(names, entry.Name())
		}
		slices.Sort(names)
		if len(names) == 0 {
			summary, readErr := trace.ReadSummary(filepath.Join(root, "latest", trace.FileName))
			return summary, "latest", readErr
		}
		name = names[len(names)-1]
	}
	if !safeTraceName(name) {
		return trace.Summary{}, "", fmt.Errorf("goatest: invalid trace run %q", name)
	}
	path := filepath.Join(root, name)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return trace.Summary{}, "", fmt.Errorf("goatest: trace run %q is a symbolic link", name)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return trace.Summary{}, "", err
	}
	summary, err := trace.ReadSummary(path)
	return summary, name, err
}

func traceSummaryEvidence(id string, summary trace.Summary) []report.Evidence {
	status := "complete"
	switch {
	case summary.Missing:
		status = "missing"
	case !summary.HasRunEnd:
		status = "incomplete-no-run-end"
	case summary.EventsDropped != 0 || summary.MissingSequences != 0:
		status = "lossy"
	}
	result := []report.Evidence{{
		Kind: "trace-summary", ID: id, Status: status,
		Detail: fmt.Sprintf("path=%s events=%d first-seq=%d last-seq=%d missing-sequences=%d run-end=%t dropped=%d verdict=%s",
			summary.Path, summary.Events, summary.FirstSequence, summary.LastSequence, summary.MissingSequences,
			summary.HasRunEnd, summary.EventsDropped, summary.Verdict),
	}}
	keys := make([]string, 0, len(summary.Counts))
	for kind := range summary.Counts {
		keys = append(keys, kind)
	}
	slices.Sort(keys)
	for _, kind := range keys {
		result = append(result, report.Evidence{Kind: "trace-count", ID: id + ":" + kind, Status: "observed", Detail: fmt.Sprintf("count=%d", summary.Counts[kind])})
	}
	phases := make([]string, 0, len(summary.PhaseDurationMS))
	for phase := range summary.PhaseDurationMS {
		phases = append(phases, phase)
	}
	slices.Sort(phases)
	for _, phase := range phases {
		result = append(result, report.Evidence{Kind: "trace-phase", ID: id + ":" + phase, Status: "observed", Detail: fmt.Sprintf("duration-ms=%d", summary.PhaseDurationMS[phase])})
	}
	return result
}

func safeTraceName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func allZeroCounts(values map[string]int) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func allZeroDurations(values map[string]int64) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}
