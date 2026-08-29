// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"

	"github.com/P4suta/goatest/internal/config"
)

func TestMutationJobLimitParallelizesLocalWorkAndSerializesExclusiveResources(t *testing.T) {
	if got := mutationJobLimit(Options{MutationJobs: 3}, config.Config{}); got != 3 {
		t.Fatalf("local mutation jobs = %d, want 3", got)
	}
	shared := config.Config{Resources: map[string]config.Resource{
		"postgres": {Shared: true},
	}}
	if got := mutationJobLimit(Options{MutationJobs: 3}, shared); got != 3 {
		t.Fatalf("shared-resource mutation jobs = %d, want 3", got)
	}
	exclusive := config.Config{Resources: map[string]config.Resource{
		"postgres": {Exclusive: true},
	}}
	if got := mutationJobLimit(Options{MutationJobs: 3}, exclusive); got != 1 {
		t.Fatalf("exclusive-resource mutation jobs = %d, want 1", got)
	}
	if got := mutationJobLimit(Options{}, config.Config{}); got < 1 || got > 4 {
		t.Fatalf("default mutation jobs = %d, want 1..4", got)
	}
}

func TestMutationProgressReportsFirstPercentMilestonesAndLast(t *testing.T) {
	var events []Event
	progress := mutationProgress(Options{Progress: func(event Event) {
		events = append(events, event)
	}})
	for completed := 1; completed <= 250; completed++ {
		progress(completed, 250)
	}
	if len(events) == 0 || events[0].Kind != "mutation-progress" || events[0].Detail != "1/250" {
		t.Fatalf("first progress = %+v", events)
	}
	if events[len(events)-1].Detail != "250/250" {
		t.Fatalf("last progress = %+v", events[len(events)-1])
	}
	if len(events) > 102 {
		t.Fatalf("progress emitted %d events, want at most 102", len(events))
	}
}
