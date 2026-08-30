// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gen_test

import (
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/gen"
)

type source []uint64

func (s *source) Uint64() uint64 {
	if len(*s) == 0 {
		return 0
	}
	value := (*s)[0]
	*s = (*s)[1:]
	return value
}

type tree struct {
	Value    int
	Children []tree
}

func TestCollectionsConstraintsAndRecursion(t *testing.T) {
	values := source{2, 4, 5}
	slice := gen.SliceOf(gen.IntRange(0, 9), 1, 3)
	if got := slice.Generate(&values); !slices.Equal(got, []int{4, 5, 0}) {
		t.Errorf("slice = %v, want [4 5 0]", got)
	}

	even := gen.Filter(gen.IntRange(0, 9), func(value int) bool { return value%2 == 0 }, 0, 4)
	choices := source{3, 5, 8}
	if got := even.Generate(&choices); got != 8 {
		t.Errorf("filtered value = %d, want 8", got)
	}

	leaf := gen.Map(gen.IntRange(0, 9), func(value int) tree { return tree{Value: value} }, func(tree) []tree { return nil })
	recursive := gen.Recursive(leaf, 2, func(child gen.Generator[tree]) gen.Generator[tree] {
		return gen.Map(gen.SliceOf(child, 1, 2), func(children []tree) tree {
			return tree{Children: children}
		}, func(value tree) []tree { return value.Children })
	})
	recursiveChoices := source{1, 0, 7, 0, 3, 4, 5}
	if got := recursive.Generate(&recursiveChoices); len(got.Children) == 0 {
		t.Errorf("recursive value = %+v, want a branch", got)
	}
}

func TestStateMachineRecordsStatesAndShrinksToPrefixes(t *testing.T) {
	machine := gen.StateMachine(0, []gen.MachineStep[int]{
		{Name: "inc", Apply: func(value int) int { return value + 1 }},
		{Name: "double", Enabled: func(value int) bool { return value > 0 }, Apply: func(value int) int { return value * 2 }},
	}, 2, 4)
	choices := source{1, 0, 1, 0, 1}
	trace := machine.Generate(&choices)
	if len(trace.Commands) < 2 || len(trace.States) != len(trace.Commands)+1 {
		t.Fatalf("trace = %+v", trace)
	}
	shrinks := machine.Shrink(trace)
	if len(shrinks) == 0 || len(shrinks[0].Commands) >= len(trace.Commands) {
		t.Errorf("shrinks = %+v, want shorter command traces", shrinks)
	}
}

func TestIntegerAndStringGenerationAndShrinkingAreDeterministic(t *testing.T) {
	ints := source{7}
	integer := gen.IntRange(-2, 2)
	if got := integer.Generate(&ints); got != 0 {
		t.Errorf("integer = %d, want 0", got)
	}
	if got := integer.Shrink(2); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("integer shrink = %v, want [0 1]", got)
	}

	bytes := source{3, 0, 1, 2}
	text := gen.StringRange(0, 8)
	if got := text.Generate(&bytes); got != "abc" {
		t.Errorf("string = %q, want abc", got)
	}
	if got := text.Shrink("abcd"); !slices.Equal(got, []string{"", "ab", "abc"}) {
		t.Errorf("string shrink = %q, want empty/half/prefix", got)
	}
}

func TestGeneratorsAndShrinksPreserveExtremeRangeConstraints(t *testing.T) {
	extreme := gen.IntRange(math.MinInt, math.MaxInt)
	choices := source{0, math.MaxUint64}
	for range 2 {
		value := extreme.Generate(&choices)
		if value < math.MinInt || value > math.MaxInt {
			t.Fatalf("extreme value = %d", value)
		}
	}
	positive := gen.IntRange(10, 20)
	for _, value := range positive.Shrink(20) {
		if value < 10 || value > 20 {
			t.Fatalf("positive shrink escaped range: %v", positive.Shrink(20))
		}
	}
	boundedText := gen.StringRange(2, 8)
	for _, value := range boundedText.Shrink("abcdef") {
		if len(value) < 2 || len(value) > 8 {
			t.Fatalf("string shrink escaped length range: %q", boundedText.Shrink("abcdef"))
		}
	}
}

func TestIntegerRangesAndShrinksCoverEveryBoundaryDirection(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		minimum    int
		maximum    int
		choices    source
		wantValues []int
	}{
		{name: "reversed", minimum: 5, maximum: -2, choices: source{0, 7}, wantValues: []int{-2, 5}},
		{name: "full-width", minimum: math.MinInt, maximum: math.MaxInt, choices: source{0, math.MaxUint64}, wantValues: []int{math.MinInt, math.MaxInt}},
		{name: "singleton", minimum: 4, maximum: 4, choices: source{0, math.MaxUint64}, wantValues: []int{4, 4}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			generator := gen.IntRange(testCase.minimum, testCase.maximum)
			for _, want := range testCase.wantValues {
				if got := generator.Generate(&testCase.choices); got != want {
					t.Fatalf("generated %d, want %d", got, want)
				}
			}
		})
	}

	for _, testCase := range []struct {
		name      string
		generator gen.Generator[int]
		value     int
		want      []int
	}{
		{name: "positive", generator: gen.IntRange(10, 20), value: 20, want: []int{10, 15, 19}},
		{name: "negative", generator: gen.IntRange(-20, -10), value: -20, want: []int{-10, -15, -19}},
		{name: "crossing-positive", generator: gen.IntRange(-5, 5), value: 5, want: []int{0, 2, 4}},
		{name: "crossing-negative", generator: gen.IntRange(-5, 5), value: -5, want: []int{0, -2, -4}},
		{name: "clamped-high", generator: gen.IntRange(10, 20), value: 999, want: []int{10, 15, 19}},
		{name: "at-goal", generator: gen.IntRange(-5, 5), value: 0, want: nil},
	} {
		t.Run("shrink-"+testCase.name, func(t *testing.T) {
			if got := testCase.generator.Shrink(testCase.value); !slices.Equal(got, testCase.want) {
				t.Fatalf("shrinks = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestStringRangeNormalizesBoundsAndShrinksExactPrefixes(t *testing.T) {
	emptyChoices := source{99}
	if got := gen.StringRange(-2, -5).Generate(&emptyChoices); got != "" {
		t.Fatalf("negative bounds generated %q", got)
	}
	fixedChoices := source{99, 0, 25, 26}
	if got := gen.StringRange(3, 1).Generate(&fixedChoices); got != "aza" {
		t.Fatalf("normalized fixed range generated %q, want aza", got)
	}
	bounded := gen.StringRange(2, 8)
	if got := bounded.Shrink("abcdef"); !slices.Equal(got, []string{"ab", "abc", "abcde"}) {
		t.Fatalf("string shrinks = %q", got)
	}
	if got := bounded.Shrink("ab"); got != nil {
		t.Fatalf("minimum string shrank to %q", got)
	}
	if got := gen.StringRange(0, 8).Shrink("ab"); !slices.Equal(got, []string{"", "a"}) {
		t.Fatalf("duplicate string shrink lengths were not removed: %q", got)
	}
}

func TestCombinatorsDelegateGenerationAndPreserveConstraintsWhenShrinking(t *testing.T) {
	constant := gen.Constant("fixed")
	choices := source{123}
	if got := constant.Generate(&choices); got != "fixed" || constant.Shrink(got) != nil {
		t.Fatalf("constant generated or shrank unexpectedly: %q / %v", got, constant.Shrink(got))
	}

	mapped := gen.Map(gen.IntRange(0, 9), func(value int) string { return strings.Repeat("x", value) }, func(value string) []string { return []string{value[:len(value)/2]} })
	mapChoices := source{4}
	if got := mapped.Generate(&mapChoices); got != "xxxx" || !slices.Equal(mapped.Shrink(got), []string{"xx"}) {
		t.Fatalf("mapped generator = %q / %q", got, mapped.Shrink(got))
	}

	selected := gen.OneOf(gen.IntRange(0, 4), gen.IntRange(10, 10))
	selection := source{1, 0}
	if got := selected.Generate(&selection); got != 10 {
		t.Fatalf("OneOf generated %d, want 10", got)
	}
	if got := selected.Shrink(4); !slices.Equal(got, []int{0, 2, 3}) {
		t.Fatalf("OneOf did not delegate shrink to first generator: %v", got)
	}

	filtered := gen.Filter(gen.IntRange(0, 9), func(value int) bool { return value%2 == 0 }, 8, 2)
	failed := source{1, 3}
	if got := filtered.Generate(&failed); got != 8 {
		t.Fatalf("Filter fallback = %d, want 8", got)
	}
	oneAttempt := gen.Filter(gen.IntRange(0, 9), func(value int) bool { return value%2 == 0 }, 8, 0)
	passing := source{2}
	if got := oneAttempt.Generate(&passing); got != 2 {
		t.Fatalf("Filter normalized attempt generated %d, want 2", got)
	}
	if got := filtered.Shrink(8); !slices.Equal(got, []int{0, 4}) {
		t.Fatalf("Filter shrinks = %v, want only even candidates", got)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("Filter accepted an invalid fallback")
		}
	}()
	_ = gen.Filter(gen.IntRange(0, 9), func(value int) bool { return value%2 == 0 }, 3, 1)
}

func TestSliceShrinksAreExactIndependentPrefixes(t *testing.T) {
	generator := gen.SliceOf(gen.IntRange(0, 9), 1, 8)
	input := []int{1, 2, 3, 4}
	got := generator.Shrink(input)
	want := [][]int{{1}, {1, 2}, {1, 2, 3}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("slice shrinks = %v, want %v", got, want)
	}
	got[0][0] = 99
	if input[0] != 1 {
		t.Fatal("slice shrink aliases its input")
	}
	if minimum := generator.Shrink([]int{1}); minimum != nil {
		t.Fatalf("minimum slice shrank to %v", minimum)
	}
	if duplicate := gen.SliceOf(gen.Constant(1), 0, 8).Shrink([]int{1, 2}); !slices.EqualFunc(duplicate, [][]int{{}, {1}}, slices.Equal) {
		t.Fatalf("duplicate slice shrink lengths were not removed: %v", duplicate)
	}
	negativeBounds := gen.SliceOf(gen.Constant(1), -2, -4)
	choices := source{0}
	if value := negativeBounds.Generate(&choices); len(value) != 0 {
		t.Fatalf("negative bounds generated %v", value)
	}
}

func TestStateMachineHonoursAvailabilityAndRebuildsValidShrinkStates(t *testing.T) {
	machine := gen.StateMachine(0, []gen.MachineStep[int]{
		{Name: "disabled", Enabled: func(int) bool { return false }, Apply: func(value int) int { return value + 100 }},
		{Name: "inc", Apply: func(value int) int { return value + 1 }},
		{Name: "double", Enabled: func(value int) bool { return value > 0 }, Apply: func(value int) int { return value * 2 }},
	}, 0, 4)
	trace := gen.MachineTrace[int]{Commands: []string{"disabled", "inc", "unknown", "inc"}, States: []int{999}}
	shrinks := machine.Shrink(trace)
	if len(shrinks) != 3 {
		t.Fatalf("state machine shrinks = %+v", shrinks)
	}
	if !slices.Equal(shrinks[0].Commands, []string{}) || !slices.Equal(shrinks[0].States, []int{0}) {
		t.Fatalf("empty prefix = %+v", shrinks[0])
	}
	if !slices.Equal(shrinks[1].Commands, []string{"disabled", "inc"}) || !slices.Equal(shrinks[1].States, []int{0, 1}) {
		t.Fatalf("two-command prefix = %+v", shrinks[1])
	}
	if !slices.Equal(shrinks[2].Commands, []string{"disabled", "inc", "unknown"}) || !slices.Equal(shrinks[2].States, []int{0, 1}) {
		t.Fatalf("three-command prefix = %+v", shrinks[2])
	}
	duplicateShrinks := machine.Shrink(gen.MachineTrace[int]{Commands: []string{"inc", "inc"}})
	if len(duplicateShrinks) != 2 || len(duplicateShrinks[0].Commands) != 0 || len(duplicateShrinks[1].Commands) != 1 {
		t.Fatalf("duplicate state-machine shrink lengths were not removed: %+v", duplicateShrinks)
	}
	minimumMachine := gen.StateMachine(0, []gen.MachineStep[int]{{Name: "inc", Apply: func(value int) int { return value + 1 }}}, 2, 4)
	if got := minimumMachine.Shrink(gen.MachineTrace[int]{Commands: []string{"inc", "inc"}}); got != nil {
		t.Fatalf("minimum state-machine trace shrank to %+v", got)
	}

	generated := gen.StateMachine(0, []gen.MachineStep[int]{
		{Name: "inc", Apply: func(value int) int { return value + 1 }},
		{Name: "double", Enabled: func(value int) bool { return value > 0 }, Apply: func(value int) int { return value * 2 }},
	}, 3, 3)
	choices := source{0, 0, 1, 0}
	got := generated.Generate(&choices)
	if !slices.Equal(got.Commands, []string{"inc", "double", "inc"}) || !slices.Equal(got.States, []int{0, 1, 2, 3}) {
		t.Fatalf("generated trace = %+v", got)
	}

	blocked := gen.StateMachine(0, []gen.MachineStep[int]{{
		Name: "dec", Enabled: func(value int) bool { return value > 0 }, Apply: func(value int) int { return value - 1 },
	}}, 1, 1)
	blockedChoices := source{0}
	if got := blocked.Generate(&blockedChoices); len(got.Commands) != 0 || !slices.Equal(got.States, []int{0}) {
		t.Fatalf("blocked trace = %+v", got)
	}
}

func FuzzGeneratorRangeAndShrinkConstraints(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("goatest-generator-v1"))
	f.Fuzz(func(t *testing.T, input []byte) {
		var choices [16]byte
		copy(choices[:], input)
		minimum := int(int32(binary.LittleEndian.Uint32(choices[0:4])))
		maximum := int(int32(binary.LittleEndian.Uint32(choices[4:8])))
		lower, upper := minimum, maximum
		if upper < lower {
			lower, upper = upper, lower
		}
		values := source{binary.LittleEndian.Uint64(choices[8:16])}
		generator := gen.IntRange(minimum, maximum)
		value := generator.Generate(&values)
		if value < lower || value > upper {
			t.Fatalf("generated %d outside [%d,%d]", value, lower, upper)
		}
		for _, candidate := range generator.Shrink(value) {
			if candidate < lower || candidate > upper || candidate == value {
				t.Fatalf("shrink %d from %d outside [%d,%d] or unchanged", candidate, value, lower, upper)
			}
		}
	})
}
