// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/evidence"
)

func targetInputsFixture() evidence.TargetInputs {
	return evidence.TargetInputs{
		Files:            map[string]string{"value.go": "aaa", "value_test.go": "bbb"},
		Dependencies:     map[string]string{"example.com/dep": "v1.2.3:sum"},
		Toolchain:        "go1.26.6",
		Platform:         "linux/amd64",
		Environment:      []string{"B=2", "A=1"},
		Contract:         "standard-v1",
		TestArgs:         []string{"-run", "TestValue"},
		BuildTags:        []string{"integration", "slow"},
		CommandTimeout:   30 * time.Second,
		TargetTimeout:    5 * time.Minute,
		GoatestVersion:   "v0.1.0",
		GoMutantsVersion: "v0.1.0",
		Corpus:           map[string]string{"testdata/fuzz/FuzzValue/seed": "ccc"},
	}
}

// isBehaviorKey reports whether a key is the 64 lowercase hex characters every
// consumer of the store is allowed to assume.
func isBehaviorKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, character := range key {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func TestTargetBehaviorKeyIsDeterministicAndEveryInputInvalidatesIt(t *testing.T) {
	t.Parallel()
	base := targetInputsFixture()
	want := evidence.TargetBehaviorKey(base)
	if again := evidence.TargetBehaviorKey(base.Clone()); again != want {
		t.Fatalf("TargetBehaviorKey is not deterministic: %s != %s", again, want)
	}
	mutations := []func(*evidence.TargetInputs){
		func(inputs *evidence.TargetInputs) { inputs.Files["value.go"] = "changed" },
		func(inputs *evidence.TargetInputs) { inputs.Dependencies["example.com/dep"] = "v2.0.0:sum" },
		func(inputs *evidence.TargetInputs) { inputs.Toolchain = "go1.27.0" },
		func(inputs *evidence.TargetInputs) { inputs.Platform = "darwin/arm64" },
		func(inputs *evidence.TargetInputs) { inputs.Environment = []string{"A=changed", "B=2"} },
		func(inputs *evidence.TargetInputs) { inputs.Contract = "deep-v1" },
		func(inputs *evidence.TargetInputs) { inputs.TestArgs = []string{"-run", "TestOther"} },
		func(inputs *evidence.TargetInputs) { inputs.BuildTags = []string{"integration"} },
		func(inputs *evidence.TargetInputs) { inputs.CommandTimeout = 31 * time.Second },
		func(inputs *evidence.TargetInputs) { inputs.TargetTimeout = 6 * time.Minute },
		func(inputs *evidence.TargetInputs) { inputs.GoatestVersion = "v0.2.0" },
		func(inputs *evidence.TargetInputs) { inputs.GoMutantsVersion = "v0.2.0" },
		func(inputs *evidence.TargetInputs) { inputs.Corpus["testdata/fuzz/FuzzValue/seed"] = "changed" },
		func(inputs *evidence.TargetInputs) { inputs.Files["added.go"] = "ddd" },
		func(inputs *evidence.TargetInputs) { delete(inputs.Files, "value_test.go") },
		func(inputs *evidence.TargetInputs) { inputs.TestArgs = []string{"TestValue", "-run"} },
		func(inputs *evidence.TargetInputs) { inputs.TestArgs = nil },
		func(inputs *evidence.TargetInputs) { inputs.BuildTags = nil },
	}
	for index, mutate := range mutations {
		candidate := base.Clone()
		mutate(&candidate)
		if got := evidence.TargetBehaviorKey(candidate); got == want {
			t.Errorf("mutation %d did not invalidate the key", index)
		}
	}
	// A configured but empty argument list is not the same configuration as no
	// argument list, so the two must not share a verdict.
	absent := base.Clone()
	absent.TestArgs = nil
	present := base.Clone()
	present.TestArgs = []string{}
	if evidence.TargetBehaviorKey(absent) == evidence.TargetBehaviorKey(present) {
		t.Error("an absent TestArgs and an empty TestArgs share a key")
	}
}

func TestTargetBehaviorKeyOrdersFilesAndEnvironmentIndependently(t *testing.T) {
	t.Parallel()
	base := targetInputsFixture()
	want := evidence.TargetBehaviorKey(base)
	if !isBehaviorKey(want) {
		t.Fatalf("TargetBehaviorKey = %q, want 64 lowercase hex characters", want)
	}
	reordered := base.Clone()
	reordered.Environment = []string{"A=1", "B=2"}
	if got := evidence.TargetBehaviorKey(reordered); got != want {
		t.Errorf("environment order changed the key: %s != %s", got, want)
	}
	// The caller's slice is theirs: sorting the environment must not reach back
	// into it.
	unsorted := base.Clone()
	unsorted.Environment = []string{"B=2", "A=1"}
	evidence.TargetBehaviorKey(unsorted)
	if !reflect.DeepEqual(unsorted.Environment, []string{"B=2", "A=1"}) {
		t.Errorf("TargetBehaviorKey reordered the caller's environment: %v", unsorted.Environment)
	}
	insertionOrdered := base.Clone()
	insertionOrdered.Files = map[string]string{}
	insertionOrdered.Files["value_test.go"] = "bbb"
	insertionOrdered.Files["value.go"] = "aaa"
	if got := evidence.TargetBehaviorKey(insertionOrdered); got != want {
		t.Errorf("file insertion order changed the key: %s != %s", got, want)
	}
	// The behaviour key answers a different question than the run identity, so
	// the two must never collide on the same inputs.
	analogous := evidence.Inputs{
		Files: base.Files, Dependencies: base.Dependencies, Toolchain: base.Toolchain,
		Platform: base.Platform, Environment: base.Environment, Corpus: base.Corpus,
		Contract: base.Contract, GoatestVersion: base.GoatestVersion, GoMutantsVersion: base.GoMutantsVersion,
	}
	if evidence.Digest(analogous) == want {
		t.Error("the behaviour key equals the run digest of the analogous inputs")
	}
}

// TestTargetBehaviorKeyIgnoresJobsTraceAndKeepTemp pins the allowlist. Every
// field of TargetInputs is, by construction, part of a target's behaviour;
// anything absent from this list is by definition not. It is the ratchet that
// stops MutationJobs (parallelism does not change a result), Packages,
// PackageScope, Replay*, Changed (always default under the full-run guard),
// FuzzExecutions (fuzz evidence is never reused), and Trace or KeepTemp (ADR
// 0002: diagnostics never enter identity) from silently joining the key.
func TestTargetBehaviorKeyIgnoresJobsTraceAndKeepTemp(t *testing.T) {
	t.Parallel()
	want := []string{
		"Files", "Dependencies", "Toolchain", "Platform", "Environment", "Contract",
		"TestArgs", "BuildTags", "CommandTimeout", "TargetTimeout", "GoatestVersion",
		"GoMutantsVersion", "Corpus",
	}
	inputsType := reflect.TypeOf(evidence.TargetInputs{})
	got := make([]string, 0, inputsType.NumField())
	for index := range inputsType.NumField() {
		got = append(got, inputsType.Field(index).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TargetInputs fields = %v, want %v; a change here changes what a reused verdict means", got, want)
	}
}

func TestTargetInputsCloneIsEqualAndOwnsEveryMutableCollection(t *testing.T) {
	t.Parallel()
	original := targetInputsFixture()
	clone := original.Clone()
	if !reflect.DeepEqual(clone, original) {
		t.Fatalf("clone = %+v, want %+v", clone, original)
	}
	clone.Files["value.go"] = "two"
	clone.Dependencies["example.com/dep"] = "v2"
	clone.Environment[0] = "B=changed"
	clone.TestArgs[0] = "-count"
	clone.BuildTags[0] = "unit"
	clone.Corpus["testdata/fuzz/FuzzValue/seed"] = "two"
	if original.Files["value.go"] != "aaa" || original.Dependencies["example.com/dep"] != "v1.2.3:sum" ||
		original.Environment[0] != "B=2" || original.TestArgs[0] != "-run" ||
		original.BuildTags[0] != "integration" || original.Corpus["testdata/fuzz/FuzzValue/seed"] != "ccc" {
		t.Fatalf("mutating clone changed original: %+v", original)
	}
}

// suiteTargetsFixture is what a package's suite runs: two targets of the same
// package, each with the behaviour key it had.
func suiteTargetsFixture() []evidence.TargetKey {
	return []evidence.TargetKey{
		{Package: "example.com/module", Name: "TestValue", Kind: "test", Key: strings.Repeat("a", 64)},
		{Package: "example.com/module", Name: "FuzzValue", Kind: "fuzz", Key: strings.Repeat("b", 64)},
	}
}

// TestSuiteBehaviorKeyIsTheConjunctionOfItsTargetsAndItsOwnInputs pins what an
// unreached mutant's verdict is a statement about. The package suite runs every
// target of the package, fuzz targets included, as one command: it observes
// what all of them observe, so a change to any one of them, to which targets
// there are, or to what the package-level run itself reads is a change to the
// suite. The order the targets arrive in is not, because a suite is a set.
func TestSuiteBehaviorKeyIsTheConjunctionOfItsTargetsAndItsOwnInputs(t *testing.T) {
	t.Parallel()
	inputs := targetInputsFixture()
	base := evidence.SuiteBehaviorKey(inputs, suiteTargetsFixture())
	if !isBehaviorKey(base) {
		t.Fatalf("suite key = %q, want a sha256 digest", base)
	}
	if base == evidence.TargetBehaviorKey(inputs) {
		t.Fatal("the suite key of a package equals the behaviour key of one of its targets")
	}

	reordered := suiteTargetsFixture()
	slices.Reverse(reordered)
	if evidence.SuiteBehaviorKey(inputs, reordered) != base {
		t.Fatal("the order the targets arrive in changed the suite key")
	}
	for _, test := range []struct {
		name    string
		targets []evidence.TargetKey
		inputs  evidence.TargetInputs
	}{
		{name: "a target whose behaviour key moved", targets: func() []evidence.TargetKey {
			changed := suiteTargetsFixture()
			changed[0].Key = strings.Repeat("c", 64)
			return changed
		}()},
		{name: "a target that entered the package", targets: append(suiteTargetsFixture(),
			evidence.TargetKey{Package: "example.com/module", Name: "TestLate", Kind: "test", Key: strings.Repeat("d", 64)})},
		{name: "a target that left the package", targets: suiteTargetsFixture()[:1]},
		{name: "a target renamed to another kind", targets: func() []evidence.TargetKey {
			changed := suiteTargetsFixture()
			changed[1].Kind = "test"
			return changed
		}()},
		{name: "what the package-level run itself reads", inputs: func() evidence.TargetInputs {
			changed := targetInputsFixture()
			changed.Files["value.go"] = "edited"
			return changed
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			targets := test.targets
			if targets == nil {
				targets = suiteTargetsFixture()
			}
			changed := test.inputs
			if changed.Toolchain == "" {
				changed = inputs
			}
			if evidence.SuiteBehaviorKey(changed, targets) == base {
				t.Fatalf("changing %s left the suite key alone", test.name)
			}
		})
	}
}
