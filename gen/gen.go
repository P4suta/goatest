// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package gen provides typed deterministic generators and shrinkers.
package gen

import "strconv"

// Source supplies deterministic choices. goatest.T implements Source, keeping
// this package independent from testing and from the goatest root package.
type Source interface {
	Uint64() uint64
}

// Generator produces and shrinks values of T.
type Generator[T any] interface {
	Generate(Source) T
	Shrink(T) []T
}

type generator[T any] struct {
	generate func(Source) T
	shrink   func(T) []T
}

func (g generator[T]) Generate(source Source) T { return g.generate(source) }
func (g generator[T]) Shrink(value T) []T       { return g.shrink(value) }

// IntRange generates an integer in the inclusive [minimum, maximum] range.
func IntRange(minimum, maximum int) Generator[int] {
	if maximum < minimum {
		minimum, maximum = maximum, minimum
	}
	width := intRangeWidth(minimum, maximum)
	return generator[int]{
		generate: func(source Source) int {
			choice := source.Uint64()
			if width != 0 {
				choice %= width
			}
			if strconv.IntSize == 32 {
				return int(uint32(uint64(uint32(minimum)) + choice))
			}
			return int(uint64(minimum) + choice)
		},
		shrink: func(value int) []int { return shrinkIntRange(value, minimum, maximum) },
	}
}

func intRangeWidth(minimum, maximum int) uint64 {
	if strconv.IntSize == 32 {
		return uint64(uint32(maximum)-uint32(minimum)) + 1
	}
	difference := uint64(maximum) - uint64(minimum)
	if difference == ^uint64(0) {
		return 0
	}
	return difference + 1
}

func shrinkIntRange(value, minimum, maximum int) []int {
	if value < minimum {
		value = minimum
	}
	if value > maximum {
		value = maximum
	}
	goal := 0
	if goal < minimum {
		goal = minimum
	}
	if goal > maximum {
		goal = maximum
	}
	if value == goal {
		return nil
	}
	half := value/2 + goal/2
	toward := value
	if value > goal {
		toward--
	} else {
		toward++
	}
	values := make([]int, 0, 3)
	for _, candidate := range []int{goal, half, toward} {
		if candidate < minimum || candidate > maximum || candidate == value {
			continue
		}
		duplicate := false
		for _, existing := range values {
			if candidate == existing {
				duplicate = true
				break
			}
		}
		if !duplicate {
			values = append(values, candidate)
		}
	}
	return values
}

// String is a bounded printable-ASCII string generator.
func String() Generator[string] { return StringRange(0, 64) }

// StringRange generates a lower-case ASCII string whose length is in the
// inclusive range. Lower-case values keep the base generator predictable;
// callers can use Map when a richer alphabet is useful.
func StringRange(minimum, maximum int) Generator[string] {
	if minimum < 0 {
		minimum = 0
	}
	if maximum < minimum {
		maximum = minimum
	}
	lengths := IntRange(minimum, maximum)
	return generator[string]{
		generate: func(source Source) string {
			length := lengths.Generate(source)
			text := make([]byte, length)
			for i := range text {
				text[i] = 'a' + byte(source.Uint64()%26)
			}
			return string(text)
		},
		shrink: func(value string) []string {
			if len(value) <= minimum {
				return nil
			}
			lengths := []int{minimum, max(minimum, len(value)/2), len(value) - 1}
			seen := make(map[int]bool, len(lengths))
			values := make([]string, 0, len(lengths))
			for _, length := range lengths {
				if length < minimum || length >= len(value) || seen[length] {
					continue
				}
				seen[length] = true
				values = append(values, value[:length])
			}
			return values
		},
	}
}
