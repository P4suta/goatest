// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/evidence"
)

const (
	changedCommandTimeout = 31 * time.Second
	changedTargetTimeout  = 6 * time.Minute
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
		GoatestBuild:     "build-one",
		GoMutantsVersion: "v0.1.0",
		Corpus:           map[string]string{"testdata/fuzz/FuzzValue/seed": "ccc"},
	}
}

func isBehaviorKey(key string) bool {
	if len(key) != len(mutationDigest("a")) {
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
		func(inputs *evidence.TargetInputs) { inputs.CommandTimeout = changedCommandTimeout },
		func(inputs *evidence.TargetInputs) { inputs.TargetTimeout = changedTargetTimeout },
		func(inputs *evidence.TargetInputs) { inputs.GoatestVersion = "v0.2.0" },
		func(inputs *evidence.TargetInputs) { inputs.GoatestBuild = "build-two" },
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

	absent := base.Clone()
	absent.TestArgs = nil
	present := base.Clone()
	present.TestArgs = []string{}
	if evidence.TargetBehaviorKey(absent) == evidence.TargetBehaviorKey(present) {
		t.Error("an absent TestArgs and an empty TestArgs share a key")
	}
}

func TestBehaviorKeyContractV3Golden(t *testing.T) {
	t.Parallel()
	inputs := targetInputsFixture()
	target := evidence.TargetBehaviorKey(inputs)
	suite := evidence.SuiteBehaviorKey(inputs, suiteTargetsFixture())
	const wantTarget = "30ea61a42e0bd3ef8598b7b707d0c3755a1a8da408a1112a2e24b596ea9fd172"
	const wantSuite = "785e338cf127d2abfd3f310db020c8e09cd7791b1dac5650923efbc33e3a11eb"
	if target != wantTarget || suite != wantSuite {
		t.Fatalf("behavior keys = target %s, suite %s", target, suite)
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

	analogous := evidence.Inputs{
		Files: base.Files, Dependencies: base.Dependencies, Toolchain: base.Toolchain,
		Platform: base.Platform, Environment: base.Environment, Corpus: base.Corpus,
		Contract: base.Contract, GoatestVersion: base.GoatestVersion, GoatestBuild: base.GoatestBuild,
		GoMutantsVersion: base.GoMutantsVersion,
	}
	if evidence.Digest(analogous) == want {
		t.Error("the behaviour key equals the run digest of the analogous inputs")
	}
}

func TestTargetBehaviorKeyIgnoresJobsTraceAndKeepTemp(t *testing.T) {
	t.Parallel()
	want := []string{
		"Files", "Dependencies", "Toolchain", "Platform", "Environment", "Contract",
		"TestArgs", "BuildTags", "CommandTimeout", "TargetTimeout", "GoatestVersion",
		"GoatestBuild", "GoMutantsVersion", "Corpus",
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

func suiteTargetsFixture() []evidence.TargetKey {
	return []evidence.TargetKey{
		{Package: "example.com/module", Name: "TestValue", Kind: "test", Key: mutationDigest("a")},
		{Package: "example.com/module", Name: "FuzzValue", Kind: "fuzz", Key: mutationDigest("b")},
	}
}

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
			changed[0].Key = mutationDigest("c")
			return changed
		}()},
		{name: "a target that entered the package", targets: append(suiteTargetsFixture(),
			evidence.TargetKey{Package: "example.com/module", Name: "TestLate", Kind: "test", Key: mutationDigest("d")})},
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
