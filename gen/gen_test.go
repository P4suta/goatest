// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gen_test

import (
	"encoding/binary"
	"math"
	"slices"
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
