// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/trace"
)

const (
	packageSuiteProbePrefix    = "package-suite:"
	packageSuiteCoveragePrefix = "package-suite-coverage:"
	pairedControlProbePrefix   = "paired-control:"
)

// readBufferSize is the read buffer one line is assembled in. A line is not
// bounded by it: a route event naming every target that reaches a mutant can
// be far larger than any fixed buffer, so lines are read whole rather than
// through a scanner that would refuse the long ones.
// routePlanReused is the whole plan of a route the run resolved from an
// earlier run's evidence: the reuse itself, and nothing that ran.
const routePlanReused = "reused"

const readBufferSize = 1 << 16

// firstSequence is the number a recording gives its first event.
const firstSequence = 1

// readEvents reads a whole trace stream, refusing anything the contract does
// not allow rather than summarizing what it understood of it.
//
// The line checks are goatest-trace-v1 itself: the required fields, the
// pairing of an event type with the single payload it carries, the ranges, and
// the schema identity the run-start alone declares. The stream checks are the
// order the contract states around them — a recording opens with one run-start,
// sequence numbers only advance, and nothing follows the run-end.
//
// Two things a strict reader could refuse are deliberately accepted, because
// refusing them would refuse the traces most worth reading. A gap in the
// sequence numbers is what a dropped event leaves behind, and the run-end that
// reports the drop is on the other side of it. A stream that ends without a
// run-end at all is what a killed or crashed run leaves; the summary reports
// the absence instead.
func readEvents(reader io.Reader) ([]trace.Event, error) {
	buffered := bufio.NewReaderSize(reader, readBufferSize)
	var events []trace.Event
	var previousSeq int64
	ended := false
	for number := 1; ; number++ {
		line, readErr := buffered.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("line %d: %w", number, readErr)
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 && readErr != nil {
			break
		}
		event, err := decodeEvent(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", number, err)
		}
		if err := checkOrder(event, len(events), previousSeq, ended); err != nil {
			return nil, fmt.Errorf("line %d: %w", number, err)
		}
		previousSeq = event.Seq
		ended = event.Type == trace.TypeRunEnd
		events = append(events, event)
		if readErr != nil {
			break
		}
	}
	if len(events) == 0 {
		return nil, errors.New("the stream carries no events")
	}
	return events, nil
}

// checkOrder holds one event to the order of the stream around it.
func checkOrder(event trace.Event, kept int, previousSeq int64, ended bool) error {
	if kept == 0 {
		if event.Type != trace.TypeRunStart {
			return fmt.Errorf("the stream opens with a %s event, want a %s event", event.Type, trace.TypeRunStart)
		}
		if event.Seq != firstSequence {
			return fmt.Errorf("the stream opens at seq %d, want seq %d; the events before it are missing", event.Seq, firstSequence)
		}
		return nil
	}
	if event.Type == trace.TypeRunStart {
		return fmt.Errorf("a second %s event; one recording opens once", trace.TypeRunStart)
	}
	if ended {
		return fmt.Errorf("a %s event after the %s event that closes the recording", event.Type, trace.TypeRunEnd)
	}
	if event.Seq <= previousSeq {
		return fmt.Errorf("seq %d does not follow seq %d", event.Seq, previousSeq)
	}
	return nil
}

// decodeEvent decodes and validates one line of the stream.
//
// The line is read twice on purpose: once into the event, which rejects an
// unknown field and a field of the wrong type anywhere in the line, and once
// into its raw fields, which is the only way to tell an absent number from a
// zero one and therefore the only way to enforce a required field the contract
// names.
func decodeEvent(line []byte) (trace.Event, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return trace.Event{}, errors.New("blank line; every line of a trace is one event")
	}
	var event trace.Event
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return trace.Event{}, err
	}
	if decoder.More() {
		return trace.Event{}, errors.New("more than one value on the line")
	}
	fields, err := objectFields(line)
	if err != nil {
		return trace.Event{}, err
	}
	if err := validateEvent(event, fields); err != nil {
		return trace.Event{}, err
	}
	return event, nil
}

// objectFields returns the raw fields of a JSON object.
func objectFields(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// validateEvent holds one event to the contract every event shares: the
// required fields, the ranges, the event type, the payload pairing, and the
// schema identity.
func validateEvent(event trace.Event, fields map[string]json.RawMessage) error {
	for _, name := range []string{"seq", "type", "timestamp", "elapsed_ms"} {
		if _, present := fields[name]; !present {
			return missingField(name)
		}
	}
	if event.Seq < firstSequence {
		return fmt.Errorf("seq %d is below %d, the number a recording opens with", event.Seq, firstSequence)
	}
	if err := checkNotNegative("elapsed_ms", event.ElapsedMS); err != nil {
		return err
	}
	if err := checkTimestamp(event.Timestamp); err != nil {
		return err
	}
	payload, known := payloadOf(event.Type)
	if !known {
		return fmt.Errorf("unknown event type %q", event.Type)
	}
	if err := checkPairing(event.Type, payload, fields); err != nil {
		return err
	}
	if payload != "" && !payloadPresent(event) {
		return fmt.Errorf("a %s event carries a null %s payload", event.Type, payload)
	}
	if err := checkSchema(event, fields); err != nil {
		return err
	}
	return checkPayload(event, fields)
}

// payloadNames are the payload fields of the contract, in the order the event
// declares them, so a deviation is always reported against the same field
// first.
func payloadNames() []string {
	return []string{"phase", "exec", "mutant", "route", "probe", "progress", "artifact", "run"}
}

// payloadOf names the single payload an event type carries, empty for the
// run-start event that carries none, and reports whether the type is one the
// contract knows at all.
func payloadOf(eventType string) (string, bool) {
	switch eventType {
	case trace.TypeRunStart:
		return "", true
	case trace.TypePhaseStart, trace.TypePhaseEnd:
		return "phase", true
	case trace.TypeExec:
		return "exec", true
	case trace.TypeMutantExec:
		return "mutant", true
	case trace.TypeRoute:
		return "route", true
	case trace.TypeProbeExec:
		return "probe", true
	case trace.TypeProgress:
		return "progress", true
	case trace.TypeArtifact:
		return "artifact", true
	case trace.TypeRunEnd:
		return "run", true
	default:
		return "", false
	}
}

// payloadPresent reports whether the payload an event's type names was
// actually decoded. A JSON null leaves the field present in the raw object but
// the pointer nil, which pairing alone cannot tell from a real payload.
func payloadPresent(event trace.Event) bool {
	switch event.Type {
	case trace.TypePhaseStart, trace.TypePhaseEnd:
		return event.Phase != nil
	case trace.TypeExec:
		return event.Exec != nil
	case trace.TypeMutantExec:
		return event.Mutant != nil
	case trace.TypeRoute:
		return event.Route != nil
	case trace.TypeProbeExec:
		return event.Probe != nil
	case trace.TypeProgress:
		return event.Progress != nil
	case trace.TypeArtifact:
		return event.Artifact != nil
	case trace.TypeRunEnd:
		return event.Run != nil
	default:
		return true
	}
}

// checkPairing holds an event to the payload its type names: that payload and
// no other one.
func checkPairing(eventType, payload string, fields map[string]json.RawMessage) error {
	for _, name := range payloadNames() {
		_, present := fields[name]
		switch {
		case present && name != payload:
			return fmt.Errorf("a %s event carries a %s payload", eventType, name)
		case !present && name == payload:
			return fmt.Errorf("a %s event carries no %s payload", eventType, name)
		}
	}
	return nil
}

// checkSchema holds the format identity to the one event that declares it.
func checkSchema(event trace.Event, fields map[string]json.RawMessage) error {
	_, present := fields["schema"]
	if event.Type != trace.TypeRunStart {
		if present {
			return fmt.Errorf("a %s event carries a schema field, which the %s event declares alone",
				event.Type, trace.TypeRunStart)
		}
		return nil
	}
	if !present {
		return missingField("schema")
	}
	if event.Schema != trace.SchemaV1 {
		return fmt.Errorf("unknown trace schema %q, want %q", event.Schema, trace.SchemaV1)
	}
	return nil
}

// checkPayload holds the payload an event carries to its own part of the
// contract.
func checkPayload(event trace.Event, fields map[string]json.RawMessage) error {
	switch {
	case event.Phase != nil:
		return checkPhase(*event.Phase, fields)
	case event.Exec != nil:
		return checkExec(*event.Exec, fields)
	case event.Mutant != nil:
		return checkMutant(*event.Mutant, fields)
	case event.Route != nil:
		return checkRoute(*event.Route, fields)
	case event.Probe != nil:
		return checkProbe(*event.Probe, fields)
	case event.Progress != nil:
		return checkProgress(*event.Progress, fields)
	case event.Artifact != nil:
		return checkArtifact(*event.Artifact, fields)
	case event.Run != nil:
		return checkRun(*event.Run, fields)
	default:
		return nil
	}
}

func checkPhase(record trace.PhaseRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "phase", "name"); err != nil {
		return err
	}
	if err := checkNotEmpty("phase.name", record.Name); err != nil {
		return err
	}
	return checkNotNegative("phase.duration_ms", record.DurationMS)
}

func checkExec(record trace.ExecRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "exec", "argv", "exit_code"); err != nil {
		return err
	}
	if err := checkNotNegative("exec.timeout_ms", record.TimeoutMS); err != nil {
		return err
	}
	if err := checkNotNegative("exec.duration_ms", record.DurationMS); err != nil {
		return err
	}
	if err := checkNotNegative("exec.output_bytes", int64(record.OutputBytes)); err != nil {
		return err
	}
	if record.OutputSHA256 != "" && !isDigest(record.OutputSHA256) {
		return fmt.Errorf("exec.output_sha256 %q is no 64 character lowercase hexadecimal digest", record.OutputSHA256)
	}
	return checkEnvironmentNames(record.EnvNames)
}

// checkEnvironmentNames holds an environment description to the half a trace
// is allowed to keep: names alone, each of them once.
func checkEnvironmentNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" || strings.Contains(name, "=") {
			return fmt.Errorf("exec.env_names carries %q; a trace records names without their values", name)
		}
		if _, repeated := seen[name]; repeated {
			return fmt.Errorf("exec.env_names repeats %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func checkMutant(record trace.MutantRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "mutant", "id"); err != nil {
		return err
	}
	if err := checkNotEmpty("mutant.id", record.ID); err != nil {
		return err
	}
	if err := checkNotNegative("mutant.timeout_ms", record.TimeoutMS); err != nil {
		return err
	}
	return checkNotNegative("mutant.duration_ms", record.DurationMS)
}

func checkRoute(record trace.RouteRecord, fields map[string]json.RawMessage) error {
	inner, err := requiredFields(fields, "route", "path", "reason")
	if err != nil {
		return err
	}
	if record.Reason != trace.ReasonCoverageReaching &&
		record.Reason != trace.ReasonProbeReaching && record.Reason != trace.ReasonUnreached {
		return fmt.Errorf("unknown route reason %q, want %q, %q, or %q",
			record.Reason, trace.ReasonCoverageReaching, trace.ReasonProbeReaching, trace.ReasonUnreached)
	}
	// The routing labels are additive, so an empty one is a recording made
	// before the field existed rather than a deviation. A value outside the
	// contract is a deviation, because a summary that counted it would be
	// counting a label nothing produces.
	if record.Granularity != "" &&
		record.Granularity != trace.GranularityBlock && record.Granularity != trace.GranularityFile {
		return fmt.Errorf("unknown route granularity %q, want %q or %q",
			record.Granularity, trace.GranularityBlock, trace.GranularityFile)
	}
	if record.Fallback != "" &&
		record.Fallback != trace.FallbackPositionUnknown && record.Fallback != trace.FallbackOutsideBlocks {
		return fmt.Errorf("unknown route fallback %q, want %q or %q",
			record.Fallback, trace.FallbackPositionUnknown, trace.FallbackOutsideBlocks)
	}
	// A fallback names why a decision by block dropped back to the file, so a
	// route recording one that was decided otherwise contradicts itself.
	if record.Fallback != "" && record.Granularity != trace.GranularityFile {
		return fmt.Errorf("route fallback %q on granularity %q, want granularity %q: a fallback is what dropped the route to the file",
			record.Fallback, record.Granularity, trace.GranularityFile)
	}
	if err := checkDischarges(record); err != nil {
		return err
	}
	if err := checkProbeRouting(record, inner); err != nil {
		return err
	}
	if err := checkReuse(record); err != nil {
		return err
	}
	if err := checkNotNegative("route.line", int64(record.Line)); err != nil {
		return err
	}
	if err := checkNotNegative("route.column", int64(record.Column)); err != nil {
		return err
	}
	if err := checkNotNegative("route.file_candidates", int64(record.FileCandidates)); err != nil {
		return err
	}
	// The granularity is what marks a route as carrying its routing metadata,
	// so a column or a candidate count without one is a route the summary
	// would read as metadata-free while it carries some. Presence is what
	// matters, not the value: a recorded zero is metadata too.
	if record.Granularity == "" {
		for _, field := range []string{"column", "file_candidates", "discharged", "probe_reaching", "suite_coverage", "suite_reached", "suite_probe", "probed"} {
			if _, present := inner[field]; present {
				return fmt.Errorf("route %s recorded without a granularity: the granularity is what marks a route as carrying its routing metadata", field)
			}
		}
	}
	return nil
}

func checkProbeRouting(record trace.RouteRecord, fields map[string]json.RawMessage) error {
	_, recovered := fields["probe_reaching"]
	if recovered {
		if len(record.ProbeReaching) == 0 {
			return errors.New("route probe_reaching is empty: a positive probe route names at least one recovered target")
		}
		if record.Reason != trace.ReasonProbeReaching || !record.Probed {
			return fmt.Errorf("route records probe-reaching targets with reason %q and probed=%t, want reason %q and probed=true",
				record.Reason, record.Probed, trace.ReasonProbeReaching)
		}
		reaching := make(map[string]bool, len(record.ReachingTargets))
		for _, target := range record.ReachingTargets {
			reaching[target] = true
		}
		seen := make(map[string]bool, len(record.ProbeReaching))
		for _, target := range record.ProbeReaching {
			if target == "" || seen[target] || !reaching[target] {
				return fmt.Errorf("route probe_reaching target %q is empty, repeated, or absent from reaching_targets", target)
			}
			seen[target] = true
		}
	} else if record.Reason == trace.ReasonProbeReaching {
		return errors.New("route has reason probe-reaching without naming probe_reaching targets")
	}
	_, suiteCoverage := fields["suite_coverage"]
	if suiteCoverage {
		if !strings.HasPrefix(record.SuiteCoverage, packageSuiteCoveragePrefix) ||
			len(record.SuiteCoverage) == len(packageSuiteCoveragePrefix) || record.Granularity != trace.GranularityBlock {
			return fmt.Errorf("route suite_coverage %q on granularity %q, want a package-suite-coverage identity on block granularity",
				record.SuiteCoverage, record.Granularity)
		}
	}
	if _, reached := fields["suite_reached"]; reached && (!suiteCoverage || !record.SuiteReached) {
		return fmt.Errorf("route suite_reached=%t without a suite_coverage control", record.SuiteReached)
	}
	if _, recorded := fields["suite_probe"]; recorded {
		if !strings.HasPrefix(record.SuiteProbe, packageSuiteProbePrefix) ||
			len(record.SuiteProbe) == len(packageSuiteProbePrefix) || !record.Probed {
			return fmt.Errorf("route suite_probe %q with probed=%t, want a package-suite identity and probed=true",
				record.SuiteProbe, record.Probed)
		}
	}
	return nil
}

// checkReuse holds a reused route to the biconditional the contract states.
// Nothing ran for a mutant the run resolved from an earlier run's evidence, so
// the plan of a reused route is the reuse and nothing else, and a plan that is
// the reuse belongs to a route that says it was reused. A reader told by one
// field and not the other is reading a recording that contradicts itself, and
// would count an execution that never happened or miss one that did.
func checkReuse(record trace.RouteRecord) error {
	planned := slices.Equal(record.Plan, []string{routePlanReused})
	if record.Reused == planned {
		return nil
	}
	if record.Reused {
		return fmt.Errorf("route reused with plan %v, want the plan %q alone: nothing runs for a reused mutant",
			record.Plan, routePlanReused)
	}
	return fmt.Errorf("route plans %q without saying it was reused: the reuse is one fact, stated in both fields",
		routePlanReused)
}

// checkDischarges holds the proofs that removed a target from a reaching set to
// the vocabulary the contract names, and to the accounting behind it: a
// discharge names one target and the proof that removed it, a discharged
// target is one the route no longer reaches, so a route naming the same target
// on both sides contradicts itself, and a target is removed by one proof, so a
// route naming the same target twice would be counted twice. One route may
// carry both proofs, because the reason is read per entry.
//
// It holds them to their prerequisites as well. The reaching set a proof
// removes a target from is the one the blocks decided, so a route recording any
// discharge was decided by block; and the infection proof is the probe pass's
// measurement of this mutant, so a route recording that one was probed. Either
// contradiction is a route the summary would otherwise count as a proof.
func checkDischarges(record trace.RouteRecord) error {
	if len(record.Discharged) == 0 {
		return nil
	}
	reaching := make(map[string]struct{}, len(record.ReachingTargets))
	for _, target := range record.ReachingTargets {
		reaching[target] = struct{}{}
	}
	discharged := make(map[string]struct{}, len(record.Discharged))
	for _, discharge := range record.Discharged {
		if err := checkNotEmpty("route.discharged.target", discharge.Target); err != nil {
			return err
		}
		if discharge.Reason != trace.DischargeBranchNeverTaken && discharge.Reason != trace.DischargeNeverInfected {
			return fmt.Errorf("unknown route discharge reason %q, want %q or %q",
				discharge.Reason, trace.DischargeBranchNeverTaken, trace.DischargeNeverInfected)
		}
		if record.Granularity != trace.GranularityBlock {
			return fmt.Errorf("route discharged %q on granularity %q, want granularity %q: a proof removes a target from a reaching set the blocks decided",
				discharge.Target, record.Granularity, trace.GranularityBlock)
		}
		if discharge.Reason == trace.DischargeNeverInfected && !record.Probed {
			return fmt.Errorf("route discharged %q as never-infected without a probe marker: the infection proof is the probe pass's measurement, so the route it removes a target from was probed",
				discharge.Target)
		}
		if _, reached := reaching[discharge.Target]; reached {
			return fmt.Errorf("route discharged %q and reaches it: a discharged target is one the route no longer reaches",
				discharge.Target)
		}
		if _, seen := discharged[discharge.Target]; seen {
			return fmt.Errorf("route discharged %q twice: a proof removes a target once",
				discharge.Target)
		}
		discharged[discharge.Target] = struct{}{}
	}
	return nil
}

// checkProbe holds one probe execution to its part of the contract: it names
// the target that ran and the status it returned with, its timings are
// durations, its outcome is one the contract names, and it ended in exactly
// one way — with that outcome, or with the error that stopped it before one.
// A record saying neither describes no execution, and one saying both would
// be counted as an error by one reader and as a measurement by another.
func checkProbe(record trace.ProbeRecord, fields map[string]json.RawMessage) error {
	probe, err := requiredFields(fields, "probe", "target", "exit_code")
	if err != nil {
		return err
	}
	if err := checkNotEmpty("probe.target", record.Target); err != nil {
		return err
	}
	_, suiteRecorded := probe["suite"]
	_, infectionsRecorded := probe["infected"]
	if record.Control {
		target := pairedControlProbePrefix + record.Package
		if record.Package == "" {
			target = pairedControlProbePrefix + "all"
		}
		if record.Target != target {
			return fmt.Errorf("paired control target %q in package %q, want %q for that exact package",
				record.Target, record.Package, target)
		}
		if suiteRecorded || infectionsRecorded {
			return errors.New("paired control carries suite or infected: a control is neither a routing suite nor a source of infection facts")
		}
	} else if strings.HasPrefix(record.Target, pairedControlProbePrefix) {
		return fmt.Errorf("probe target %q has a paired-control identity without control=true", record.Target)
	}
	if record.Suite {
		if !strings.HasPrefix(record.Target, packageSuiteProbePrefix) ||
			len(record.Target) == len(packageSuiteProbePrefix) || record.Package == "" ||
			record.Target != packageSuiteProbePrefix+record.Package {
			return fmt.Errorf("suite probe target %q in package %q, want the package-suite identity of that exact package",
				record.Target, record.Package)
		}
	} else if strings.HasPrefix(record.Target, packageSuiteProbePrefix) {
		return fmt.Errorf("probe target %q has a package-suite identity without suite=true", record.Target)
	}
	if err := checkNotNegative("probe.timeout_ms", record.TimeoutMS); err != nil {
		return err
	}
	if err := checkNotNegative("probe.duration_ms", record.DurationMS); err != nil {
		return err
	}
	switch record.Outcome {
	case "", trace.ProbeOutcomeMeasured, trace.ProbeOutcomeTestFailed,
		trace.ProbeOutcomeTimedOut, trace.ProbeOutcomeUnavailable:
	default:
		return fmt.Errorf("unknown probe outcome %q, want %q, %q, %q or %q", record.Outcome,
			trace.ProbeOutcomeMeasured, trace.ProbeOutcomeTestFailed,
			trace.ProbeOutcomeTimedOut, trace.ProbeOutcomeUnavailable)
	}
	// Presence is read from the line rather than from the decoded record, as
	// the schema reads it: an empty error and an empty infection list are
	// fields the record carries, not fields it left out.
	_, outcome := probe["outcome"]
	_, failure := probe["error"]
	switch {
	case outcome && failure:
		return errors.New("probe carries both an outcome and an error: an execution reached an outcome or was stopped by an error, never both")
	case !outcome && !failure:
		return errors.New("probe carries neither an outcome nor an error: an execution reached an outcome or was stopped by an error")
	case failure:
		if err := checkNotEmpty("probe.error", record.Error); err != nil {
			return err
		}
	}
	return checkInfections(record, infectionsRecorded)
}

// checkInfections holds the mutants a probe execution infected to the
// accounting behind them: a mutant is named by an identity, an execution
// infects a mutant once, so a record naming the same one twice would be
// counted twice, and only a measured execution observed anything at all, so
// infections beside any other outcome — an empty list included, which is the
// claim that the execution measured and found nothing — are facts an
// execution that measured none claims. recorded says whether the line carries
// the field at all, which the decoded record cannot tell from an empty list.
func checkInfections(record trace.ProbeRecord, recorded bool) error {
	if !recorded {
		return nil
	}
	if record.Outcome != trace.ProbeOutcomeMeasured {
		return fmt.Errorf("probe recorded infections with outcome %q, want outcome %q: only a measured execution observed a mutant",
			record.Outcome, trace.ProbeOutcomeMeasured)
	}
	infected := make(map[string]struct{}, len(record.Infected))
	for _, mutant := range record.Infected {
		if err := checkNotEmpty("probe.infected", mutant); err != nil {
			return err
		}
		if _, seen := infected[mutant]; seen {
			return fmt.Errorf("probe infected %q twice: an execution infects a mutant once", mutant)
		}
		infected[mutant] = struct{}{}
	}
	return nil
}

func checkProgress(record trace.ProgressRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "progress", "kind"); err != nil {
		return err
	}
	return checkNotEmpty("progress.kind", record.Kind)
}

func checkArtifact(record trace.ArtifactRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "artifact", "kind", "path"); err != nil {
		return err
	}
	if err := checkNotEmpty("artifact.kind", record.Kind); err != nil {
		return err
	}
	return checkNotEmpty("artifact.path", record.Path)
}

func checkRun(record trace.RunRecord, fields map[string]json.RawMessage) error {
	if _, err := requiredFields(fields, "run", "events_emitted", "events_dropped"); err != nil {
		return err
	}
	if err := checkNotNegative("run.events_emitted", record.EventsEmitted); err != nil {
		return err
	}
	return checkNotNegative("run.events_dropped", record.EventsDropped)
}

// requiredFields returns the raw fields of a payload once every field the
// contract requires of it is present.
func requiredFields(fields map[string]json.RawMessage, payload string, required ...string) (map[string]json.RawMessage, error) {
	inner, err := objectFields(fields[payload])
	if err != nil {
		return nil, err
	}
	for _, name := range required {
		if _, present := inner[name]; !present {
			return nil, missingField(payload + "." + name)
		}
	}
	return inner, nil
}

// missingField reports a field the contract requires and the line does not
// carry.
func missingField(name string) error {
	return fmt.Errorf("missing required field %q", name)
}

// checkNotEmpty reports a field the contract requires a value in.
func checkNotEmpty(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	return nil
}

// checkNotNegative reports a count or a duration below the range the contract
// gives it. A negative duration would silently shorten a total.
func checkNotNegative(name string, value int64) error {
	if value < 0 {
		return fmt.Errorf("%s is %d, which is below zero", name, value)
	}
	return nil
}

// checkTimestamp holds the moment an event was recorded to RFC 3339. The
// summary never reads the clock, so the check is about the stream being the
// stream it claims rather than about the value being needed.
func checkTimestamp(value string) error {
	if err := checkNotEmpty("timestamp", value); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("timestamp %q is no RFC 3339 moment", value)
	}
	return nil
}

// isDigest reports whether a value is a SHA-256 digest as the contract writes
// one: 64 lowercase hexadecimal characters.
func isDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'f':
		default:
			return false
		}
	}
	return true
}
