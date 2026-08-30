// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gen

// MachineStep is one named state transition. A nil Enabled function means the
// transition is always available.
type MachineStep[S any] struct {
	Name    string
	Enabled func(S) bool
	Apply   func(S) S
}

// MachineTrace contains the initial state and every state after each command.
type MachineTrace[S any] struct {
	Commands []string
	States   []S
}

// StateMachine generates a valid command trace in the inclusive length range.
func StateMachine[S any](initial S, steps []MachineStep[S], minimum, maximum int) Generator[MachineTrace[S]] {
	if len(steps) == 0 {
		panic("gen: StateMachine requires at least one step")
	}
	minimum = max(minimum, 0)
	maximum = max(maximum, minimum)
	lengths := IntRange(minimum, maximum)
	build := func(commands []string) MachineTrace[S] {
		state := initial
		trace := MachineTrace[S]{Commands: append([]string(nil), commands...), States: []S{state}}
		for _, name := range commands {
			for _, step := range steps {
				if step.Name != name || step.Enabled != nil && !step.Enabled(state) {
					continue
				}
				state = step.Apply(state)
				trace.States = append(trace.States, state)
				break
			}
		}
		return trace
	}
	return generator[MachineTrace[S]]{
		generate: func(source Source) MachineTrace[S] {
			state := initial
			trace := MachineTrace[S]{States: []S{state}}
			for range lengths.Generate(source) {
				enabled := make([]MachineStep[S], 0, len(steps))
				for _, step := range steps {
					if step.Enabled == nil || step.Enabled(state) {
						enabled = append(enabled, step)
					}
				}
				if len(enabled) == 0 {
					break
				}
				step := enabled[int(source.Uint64()%uint64(len(enabled)))]
				trace.Commands = append(trace.Commands, step.Name)
				state = step.Apply(state)
				trace.States = append(trace.States, state)
			}
			return trace
		},
		shrink: func(trace MachineTrace[S]) []MachineTrace[S] {
			if len(trace.Commands) <= minimum {
				return nil
			}
			lengths := []int{minimum, max(minimum, len(trace.Commands)/2), len(trace.Commands) - 1}
			seen := make(map[int]bool, len(lengths))
			shrinks := make([]MachineTrace[S], 0, len(lengths))
			for _, length := range lengths {
				if seen[length] {
					continue
				}
				seen[length] = true
				shrinks = append(shrinks, build(trace.Commands[:length]))
			}
			return shrinks
		},
	}
}
