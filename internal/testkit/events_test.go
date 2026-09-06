// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"slices"
	"testing"

	"github.com/P4suta/goatest/internal/assure"
	"github.com/P4suta/goatest/internal/testkit"
)

func eventFixture() []assure.Event {
	return []assure.Event{
		{Kind: "baseline-progress", Detail: "0/2"},
		{Kind: "snapshot", Detail: "sha256:abc"},
		{Kind: "baseline-progress", Detail: "1/2"},
		{Kind: "baseline-progress", Detail: "2/2"},
		{Kind: "mutation-progress", Detail: "1/2"},
		{Kind: "mutation-progress", Detail: "2/2"},
	}
}

func TestHasEventReportsPresence(t *testing.T) {
	t.Parallel()
	events := eventFixture()
	for _, kind := range []string{"baseline-progress", "snapshot", "mutation-progress"} {
		if !testkit.HasEvent(events, kind) {
			t.Errorf("HasEvent(%q) = false, want true", kind)
		}
	}
	if testkit.HasEvent(events, "cache-hit") {
		t.Error(`HasEvent("cache-hit") = true, want false`)
	}
	if testkit.HasEvent(nil, "snapshot") {
		t.Error("HasEvent on no events = true, want false")
	}
}

func TestCountEventCountsMatchingKinds(t *testing.T) {
	t.Parallel()
	events := eventFixture()
	for kind, want := range map[string]int{
		"baseline-progress": 3,
		"snapshot":          1,
		"mutation-progress": 2,
		"cache-hit":         0,
	} {
		if got := testkit.CountEvent(events, kind); got != want {
			t.Errorf("CountEvent(%q) = %d, want %d", kind, got, want)
		}
	}
	if got := testkit.CountEvent(nil, "snapshot"); got != 0 {
		t.Errorf("CountEvent on no events = %d, want 0", got)
	}
}

func TestEventDetailsPreservesOrderAndReturnsNilWhenAbsent(t *testing.T) {
	t.Parallel()
	events := eventFixture()
	want := []string{"0/2", "1/2", "2/2"}
	if got := testkit.EventDetails(events, "baseline-progress"); !slices.Equal(got, want) {
		t.Errorf("EventDetails = %q, want %q", got, want)
	}
	if got := testkit.EventDetails(events, "cache-hit"); got != nil {
		t.Errorf("EventDetails for an absent kind = %q, want nil", got)
	}
}
