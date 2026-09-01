// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Summary is the bounded, read-only view of one trace stream.
type Summary struct {
	Path             string
	Missing          bool
	Events           int
	FirstSequence    int64
	LastSequence     int64
	MissingSequences int64
	HasRunEnd        bool
	EventsDropped    int64
	Verdict          string
	Error            string
	Counts           map[string]int
	PhaseDurationMS  map[string]int64
}

// SummaryDiff compares two summaries without replaying either run.
type SummaryDiff struct {
	EventsDelta           int
	MissingSequencesDelta int64
	EventsDroppedDelta    int64
	BeforeVerdict         string
	AfterVerdict          string
	BeforeRunEnd          bool
	AfterRunEnd           bool
	CountDelta            map[string]int
	PhaseDurationDeltaMS  map[string]int64
}

// ReadSummary strictly reads trace.jsonl at path. path may name the stream or
// its containing directory. An absent stream is an explicit summary state.
func ReadSummary(path string) (Summary, error) {
	stream := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		stream = filepath.Join(path, FileName)
	}
	file, err := os.Open(stream)
	if errors.Is(err, os.ErrNotExist) {
		return Summary{Path: stream, Missing: true, Counts: map[string]int{}, PhaseDurationMS: map[string]int64{}}, nil
	}
	if err != nil {
		return Summary{}, fmt.Errorf("goatest: open trace %s: %w", stream, err)
	}
	defer func() { _ = file.Close() }()

	result := Summary{Path: stream, Counts: make(map[string]int), PhaseDurationMS: make(map[string]int64)}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var previous int64
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var event Event
		if err := decoder.Decode(&event); err != nil {
			return Summary{}, fmt.Errorf("goatest: decode trace event %d: %w", result.Events+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return Summary{}, fmt.Errorf("goatest: trace event %d has trailing data", result.Events+1)
		}
		if err := validateReadEvent(event); err != nil {
			return Summary{}, err
		}
		if previous != 0 {
			if event.Seq <= previous {
				return Summary{}, fmt.Errorf("goatest: trace sequence %d does not follow %d", event.Seq, previous)
			}
			result.MissingSequences += event.Seq - previous - 1
		} else {
			result.FirstSequence = event.Seq
			if event.Seq > 1 {
				result.MissingSequences += event.Seq - 1
			}
		}
		previous = event.Seq
		result.LastSequence = event.Seq
		result.Events++
		result.Counts[event.Type]++
		if event.Type == TypePhaseEnd && event.Phase != nil {
			result.PhaseDurationMS[event.Phase.Name] += event.Phase.DurationMS
		}
		if event.Type == TypeRunEnd {
			if result.HasRunEnd {
				return Summary{}, errors.New("goatest: trace contains more than one run-end event")
			}
			result.HasRunEnd = true
			result.EventsDropped = event.Run.EventsDropped
			result.Verdict = event.Run.Verdict
			result.Error = event.Run.Error
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, fmt.Errorf("goatest: read trace: %w", err)
	}
	return result, nil
}

func validateReadEvent(event Event) error {
	if event.Seq <= 0 || event.Type == "" || event.Timestamp == "" || event.ElapsedMS < 0 {
		return fmt.Errorf("goatest: trace event %d has an invalid envelope", event.Seq)
	}
	payloads := 0
	for _, present := range []bool{event.Phase != nil, event.Exec != nil, event.Mutant != nil, event.Route != nil, event.Progress != nil, event.Artifact != nil, event.Run != nil} {
		if present {
			payloads++
		}
	}
	wantPayload := true
	switch event.Type {
	case TypeRunStart:
		wantPayload = false
		if event.Schema != SchemaV1 {
			return fmt.Errorf("goatest: trace event %d has unsupported schema %q", event.Seq, event.Schema)
		}
	case TypePhaseStart, TypePhaseEnd:
		if event.Phase == nil {
			return fmt.Errorf("goatest: trace event %d is missing phase payload", event.Seq)
		}
	case TypeExec:
		if event.Exec == nil {
			return fmt.Errorf("goatest: trace event %d is missing exec payload", event.Seq)
		}
	case TypeMutantExec:
		if event.Mutant == nil {
			return fmt.Errorf("goatest: trace event %d is missing mutant payload", event.Seq)
		}
	case TypeRoute:
		if event.Route == nil {
			return fmt.Errorf("goatest: trace event %d is missing route payload", event.Seq)
		}
	case TypeProgress:
		if event.Progress == nil {
			return fmt.Errorf("goatest: trace event %d is missing progress payload", event.Seq)
		}
	case TypeArtifact:
		if event.Artifact == nil {
			return fmt.Errorf("goatest: trace event %d is missing artifact payload", event.Seq)
		}
	case TypeRunEnd:
		if event.Run == nil || event.Run.EventsEmitted < 0 || event.Run.EventsDropped < 0 {
			return fmt.Errorf("goatest: trace event %d is missing valid run payload", event.Seq)
		}
	default:
		return fmt.Errorf("goatest: trace event %d has unknown type %q", event.Seq, event.Type)
	}
	if !wantPayload && payloads != 0 || wantPayload && payloads != 1 {
		return fmt.Errorf("goatest: trace event %d has %d payloads", event.Seq, payloads)
	}
	return nil
}

func Diff(before, after Summary) SummaryDiff {
	result := SummaryDiff{
		EventsDelta:           after.Events - before.Events,
		MissingSequencesDelta: after.MissingSequences - before.MissingSequences,
		EventsDroppedDelta:    after.EventsDropped - before.EventsDropped,
		BeforeVerdict:         before.Verdict, AfterVerdict: after.Verdict,
		BeforeRunEnd: before.HasRunEnd, AfterRunEnd: after.HasRunEnd,
		CountDelta: make(map[string]int), PhaseDurationDeltaMS: make(map[string]int64),
	}
	for kind, count := range before.Counts {
		result.CountDelta[kind] -= count
	}
	for kind, count := range after.Counts {
		result.CountDelta[kind] += count
	}
	for phase, duration := range before.PhaseDurationMS {
		result.PhaseDurationDeltaMS[phase] -= duration
	}
	for phase, duration := range after.PhaseDurationMS {
		result.PhaseDurationDeltaMS[phase] += duration
	}
	return result
}
