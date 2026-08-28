// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gen

// Constant always produces value and has no smaller value.
func Constant[T any](value T) Generator[T] {
	return generator[T]{
		generate: func(Source) T { return value },
		shrink:   func(T) []T { return nil },
	}
}

// Map converts generated values. shrink supplies domain-specific candidates
// because an arbitrary conversion has no general inverse.
func Map[A, B any](base Generator[A], convert func(A) B, shrink func(B) []B) Generator[B] {
	return generator[B]{
		generate: func(source Source) B { return convert(base.Generate(source)) },
		shrink:   shrink,
	}
}

// OneOf selects one generator. Empty input is a programmer error.
func OneOf[T any](generators ...Generator[T]) Generator[T] {
	if len(generators) == 0 {
		panic("gen: OneOf requires at least one generator")
	}
	return generator[T]{
		generate: func(source Source) T {
			selected := int(source.Uint64() % uint64(len(generators)))
			return generators[selected].Generate(source)
		},
		shrink: func(value T) []T {
			return generators[0].Shrink(value)
		},
	}
}

// Filter retries base until predicate holds and otherwise returns fallback.
// fallback is validated at construction so the constraint always holds.
func Filter[T any](base Generator[T], predicate func(T) bool, fallback T, attempts int) Generator[T] {
	if !predicate(fallback) {
		panic("gen: Filter fallback does not satisfy its predicate")
	}
	if attempts < 1 {
		attempts = 1
	}
	return generator[T]{
		generate: func(source Source) T {
			for range attempts {
				value := base.Generate(source)
				if predicate(value) {
					return value
				}
			}
			return fallback
		},
		shrink: func(value T) []T {
			candidates := base.Shrink(value)
			kept := make([]T, 0, len(candidates))
			for _, candidate := range candidates {
				if predicate(candidate) {
					kept = append(kept, candidate)
				}
			}
			return kept
		},
	}
}

// SliceOf generates a slice whose length is within the inclusive range.
func SliceOf[T any](element Generator[T], minimum, maximum int) Generator[[]T] {
	if minimum < 0 {
		minimum = 0
	}
	if maximum < minimum {
		maximum = minimum
	}
	lengths := IntRange(minimum, maximum)
	return generator[[]T]{
		generate: func(source Source) []T {
			length := lengths.Generate(source)
			values := make([]T, length)
			for i := range values {
				values[i] = element.Generate(source)
			}
			return values
		},
		shrink: func(values []T) [][]T {
			if len(values) <= minimum {
				return nil
			}
			lengths := []int{minimum, max(minimum, len(values)/2), len(values) - 1}
			seen := make(map[int]bool, len(lengths))
			shrinks := make([][]T, 0, len(lengths))
			for _, length := range lengths {
				if length >= len(values) || seen[length] {
					continue
				}
				seen[length] = true
				candidate := append([]T(nil), values[:length]...)
				shrinks = append(shrinks, candidate)
			}
			return shrinks
		},
	}
}

// Recursive grows base through extend up to maximumDepth. Each level retains
// the shallower generator as an alternative, so the same trace naturally
// produces both leaves and branches.
func Recursive[T any](base Generator[T], maximumDepth int, extend func(Generator[T]) Generator[T]) Generator[T] {
	if maximumDepth < 0 {
		maximumDepth = 0
	}
	current := base
	for range maximumDepth {
		shallower := current
		current = OneOf(shallower, extend(shallower))
	}
	return current
}
