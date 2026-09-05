// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

// Recorder turns the events of a run into a stream a sink can keep.
//
// Every method is safe on a nil receiver and on a Recorder used from several
// goroutines. Sequencing and emission happen under one lock, so events reach
// the sink in strictly increasing sequence order however many goroutines record
// at once.
type Recorder struct {
	sink    Sink
	now     func() time.Time
	started time.Time

	mutex    sync.Mutex
	seq      int64
	attempts int64
	failures int64
	ended    bool
}

// New starts a recording and emits its run-start event.
//
// A nil sink returns a nil *Recorder, which is the disabled trace: callers keep
// recording unconditionally and pay nothing for it. A nil clock is the wall
// clock.
func New(sink Sink, now func() time.Time) *Recorder {
	if sink == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	started := now()
	recorder := &Recorder{sink: sink, now: now, started: started}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.emitLocked(started, Event{Type: TypeRunStart, Schema: SchemaV1})
	return recorder
}

// PhaseStart records the beginning of a phase and returns the closer that ends
// it. The closer is never nil, so callers may defer it unconditionally, and it
// emits the phase-end event once however often it runs. Phases may nest: each
// closer times its own phase.
func (recorder *Recorder) PhaseStart(name string) func() {
	if recorder == nil {
		return func() {}
	}
	recorder.mutex.Lock()
	started := recorder.now()
	recorder.emitLocked(started, Event{Type: TypePhaseStart, Phase: &PhaseRecord{Name: name}})
	recorder.mutex.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			recorder.mutex.Lock()
			defer recorder.mutex.Unlock()
			moment := recorder.now()
			recorder.emitLocked(moment, Event{
				Type:  TypePhaseEnd,
				Phase: &PhaseRecord{Name: name, DurationMS: moment.Sub(started).Milliseconds()},
			})
		})
	}
}

// Exec records one executed command. Environment names are sorted and stripped
// of any value the caller passed along, and captured output is digested rather
// than serialised, so the event carries the shape of the execution and none of
// its secrets.
func (recorder *Recorder) Exec(record ExecRecord) {
	if recorder == nil {
		return
	}
	record.EnvNames = environmentNames(record.EnvNames)
	if len(record.Output) > 0 {
		digest := sha256.Sum256(record.Output)
		record.OutputBytes = len(record.Output)
		record.OutputSHA256 = hex.EncodeToString(digest[:])
	}
	recorder.emit(Event{Type: TypeExec, Exec: &record})
}

// MutantExec records one mutant execution and its outcome.
func (recorder *Recorder) MutantExec(record MutantRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeMutantExec, Mutant: &record})
}

// Route records how a mutant was routed to the tests that run it.
func (recorder *Recorder) Route(record RouteRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeRoute, Route: &record})
}

// ProbeExec records one probe execution of a test target or package suite: how
// it ran, what became of it, and the mutants that execution infected.
func (recorder *Recorder) ProbeExec(record ProbeRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeProbeExec, Probe: &record})
}

// Progress records a progress note forwarded from the run.
func (recorder *Recorder) Progress(kind, detail string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeProgress, Progress: &ProgressRecord{Kind: kind, Detail: detail}})
}

// Artifact records a file the run wrote.
func (recorder *Recorder) Artifact(kind, path string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeArtifact, Artifact: &ArtifactRecord{Kind: kind, Path: path}})
}

// RunEnd closes the recording with the verdict, the error that ended the run if
// there was one, and the event accounting.
//
// The accounting is the honesty of a best effort trace: emitted counts the
// events the sink kept, dropped the ones it could not. A sink that reports its
// own drops is authoritative; otherwise the recorder counts the emissions that
// failed. A recording ends once, and events recorded afterwards are dropped.
func (recorder *Recorder) RunEnd(verdict string, err error) {
	if recorder == nil {
		return
	}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	if recorder.ended {
		return
	}
	record := &RunRecord{Verdict: verdict}
	if err != nil {
		record.Error = err.Error()
	}
	record.EventsDropped = recorder.droppedLocked()
	record.EventsEmitted = max(recorder.attempts-record.EventsDropped, 0)
	recorder.emitLocked(recorder.now(), Event{Type: TypeRunEnd, Run: record})
	recorder.ended = true
}

// droppedLocked reports how many events the sink did not keep, preferring the
// sink's own count over the failures the recorder observed.
func (recorder *Recorder) droppedLocked() int64 {
	if dropper, ok := recorder.sink.(Dropper); ok {
		return dropper.Dropped()
	}
	return recorder.failures
}

// emit stamps and delivers one event.
func (recorder *Recorder) emit(event Event) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.emitLocked(recorder.now(), event)
}

// emitLocked stamps an event with the next sequence number and the moment it
// was recorded, then hands it to the sink. The caller holds the lock, which is
// what keeps sequence order and delivery order the same order.
func (recorder *Recorder) emitLocked(moment time.Time, event Event) {
	if recorder.ended {
		return
	}
	recorder.seq++
	event.Seq = recorder.seq
	event.Timestamp = moment.UTC().Format(time.RFC3339Nano)
	event.ElapsedMS = moment.Sub(recorder.started).Milliseconds()
	recorder.attempts++
	if err := recorder.sink.Emit(event); err != nil {
		recorder.failures++
	}
}

// environmentNames returns the sorted, deduplicated variable names of an
// environment description. Entries arriving as name=value are reduced to their
// name, which is the only half a trace is allowed to keep.
func environmentNames(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry, "=")
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return nil
	}
	return names
}
