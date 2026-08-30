// SPDX-License-Identifier: MIT OR Apache-2.0

package testargs_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testargs"
)

func TestNormalizeCanonicalizesShortClonesAndPreservesCustomFlags(t *testing.T) {
	input := []string{"-short", "--short=false", "--test.short", "--test.parallel=3", "-custom=value"}
	got, err := testargs.Normalize(input)
	if err != nil || !slices.Equal(got, []string{"-test.short=true", "-test.short=false", "-test.short=true", "-test.parallel=3", "-custom=value"}) {
		t.Fatalf("Normalize = %v, %v", got, err)
	}
	input[0] = "changed"
	if got[0] != "-test.short=true" {
		t.Fatal("Normalize aliases its input")
	}
}

func TestNormalizeRejectsEveryAssuranceOwnedFlag(t *testing.T) {
	for _, argument := range []string{
		"-test.run=TestOther", "--test.run=TestOther", "-test.fuzz", "-test.fuzztime=1x", "-test.fuzzcachedir=tmp",
		"-test.coverprofile=other", "-test.timeout=0", "-test.count=9", "-test.v=true", "-test.skip=Slow", "-test.list=.", "-test.shuffle=on",
	} {
		if _, err := testargs.Normalize([]string{argument}); err == nil || !strings.Contains(err.Error(), "assurance-owned") {
			t.Errorf("Normalize(%q) error = %v", argument, err)
		}
	}
}
