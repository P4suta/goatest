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

func (recorder *Recorder) Prepare(phase, state, result string, duration time.Duration) {
	if recorder == nil {
		return
	}
	record := PrepareRecord{Phase: phase, State: state, Result: result}
	if state == PrepareStateFinished {
		durationMS := duration.Milliseconds()
		record.DurationMS = &durationMS
	}
	recorder.emit(Event{Type: TypePrepare, Prepare: &record})
}

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

func (recorder *Recorder) MutantExec(record MutantRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeMutantExec, Mutant: &record})
}

func (recorder *Recorder) Route(record RouteRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeRoute, Route: &record})
}

func (recorder *Recorder) ProbeExec(record ProbeRecord) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeProbeExec, Probe: &record})
}

func (recorder *Recorder) Progress(kind, detail string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeProgress, Progress: &ProgressRecord{Kind: kind, Detail: detail}})
}

func (recorder *Recorder) Artifact(kind, path string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeArtifact, Artifact: &ArtifactRecord{Kind: kind, Path: path}})
}

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

func (recorder *Recorder) droppedLocked() int64 {
	if dropper, ok := recorder.sink.(Dropper); ok {
		return dropper.Dropped()
	}
	return recorder.failures
}

func (recorder *Recorder) emit(event Event) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.emitLocked(recorder.now(), event)
}

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
