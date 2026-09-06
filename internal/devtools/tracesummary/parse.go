// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/trace"
)

const routePlanReused = "reused"

const readBufferSize = 1 << 16

const firstSequence = 1

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

func objectFields(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

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

func payloadNames() []string {
	return []string{"phase", "prepare", "exec", "mutant", "route", "probe", "progress", "artifact", "run"}
}

func payloadOf(eventType string) (string, bool) {
	switch eventType {
	case trace.TypeRunStart:
		return "", true
	case trace.TypePhaseStart, trace.TypePhaseEnd:
		return "phase", true
	case trace.TypePrepare:
		return "prepare", true
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

func payloadPresent(event trace.Event) bool {
	switch event.Type {
	case trace.TypePhaseStart, trace.TypePhaseEnd:
		return event.Phase != nil
	case trace.TypePrepare:
		return event.Prepare != nil
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

func checkPayload(event trace.Event, fields map[string]json.RawMessage) error {
	switch {
	case event.Phase != nil:
		return checkPhase(*event.Phase, fields)
	case event.Prepare != nil:
		return checkPrepare(*event.Prepare, fields)
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

func checkPrepare(record trace.PrepareRecord, fields map[string]json.RawMessage) error {
	prepare, err := requiredFields(fields, "prepare", "phase", "state")
	if err != nil {
		return err
	}
	if !knownPreparePhase(record.Phase) {
		return fmt.Errorf("unknown prepare phase %q", record.Phase)
	}
	switch record.State {
	case trace.PrepareStateStarted:
		if _, present := prepare["result"]; present {
			return errors.New("started prepare carries a result")
		}
		if _, present := prepare["duration_ms"]; present {
			return errors.New("started prepare carries a duration_ms")
		}
		return nil
	case trace.PrepareStateFinished:
		if _, err := requiredFields(fields, "prepare", "result", "duration_ms"); err != nil {
			return err
		}
		switch record.Result {
		case trace.PrepareResultSucceeded, trace.PrepareResultFailed, trace.PrepareResultSkipped:
		default:
			return fmt.Errorf("unknown prepare result %q", record.Result)
		}
		if record.DurationMS == nil {
			return errors.New("prepare.duration_ms is null")
		}
		return checkNotNegative("prepare.duration_ms", *record.DurationMS)
	default:
		return fmt.Errorf("unknown prepare state %q", record.State)
	}
}

func knownPreparePhase(phase string) bool {
	switch phase {
	case trace.PreparePhaseDiscovery,
		trace.PreparePhaseProbeSnapshot,
		trace.PreparePhaseMainValidation,
		trace.PreparePhaseMainRestoration,
		trace.PreparePhaseVerification,
		trace.PreparePhaseBinaryBuild,
		trace.PreparePhaseProbeValidation,
		trace.PreparePhaseProbeCoverageBuild,
		trace.PreparePhaseProbeRestoration:
		return true
	default:
		return false
	}
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
	inner, err := requiredFields(fields, "route", "path", "reason", "granularity")
	if err != nil {
		return err
	}
	if record.Reason != trace.ReasonCoverageReaching &&
		record.Reason != trace.ReasonProbeReaching && record.Reason != trace.ReasonUnreached {
		return fmt.Errorf("unknown route reason %q, want %q, %q, or %q",
			record.Reason, trace.ReasonCoverageReaching, trace.ReasonProbeReaching, trace.ReasonUnreached)
	}

	if record.Granularity != trace.GranularityBlock && record.Granularity != trace.GranularityFile {
		return fmt.Errorf("unknown route granularity %q, want %q or %q",
			record.Granularity, trace.GranularityBlock, trace.GranularityFile)
	}
	if record.Fallback != "" &&
		record.Fallback != trace.FallbackPositionUnknown && record.Fallback != trace.FallbackOutsideBlocks {
		return fmt.Errorf("unknown route fallback %q, want %q or %q",
			record.Fallback, trace.FallbackPositionUnknown, trace.FallbackOutsideBlocks)
	}

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
		if !strings.HasPrefix(record.SuiteCoverage, trace.PackageSuiteCoveragePrefix) ||
			len(record.SuiteCoverage) == len(trace.PackageSuiteCoveragePrefix) || record.Granularity != trace.GranularityBlock {
			return fmt.Errorf("route suite_coverage %q on granularity %q, want a package-suite-coverage identity on block granularity",
				record.SuiteCoverage, record.Granularity)
		}
	}
	if _, reached := fields["suite_reached"]; reached && (!suiteCoverage || !record.SuiteReached) {
		return fmt.Errorf("route suite_reached=%t without a suite_coverage control", record.SuiteReached)
	}
	if _, recorded := fields["suite_probe"]; recorded {
		if !strings.HasPrefix(record.SuiteProbe, trace.PackageSuiteProbePrefix) ||
			len(record.SuiteProbe) == len(trace.PackageSuiteProbePrefix) || !record.Probed {
			return fmt.Errorf("route suite_probe %q with probed=%t, want a package-suite identity and probed=true",
				record.SuiteProbe, record.Probed)
		}
	}
	if suiteCoverage && record.SuiteProbe != "" {
		coveragePackage := strings.TrimPrefix(record.SuiteCoverage, trace.PackageSuiteCoveragePrefix)
		probePackage := strings.TrimPrefix(record.SuiteProbe, trace.PackageSuiteProbePrefix)
		if coveragePackage != probePackage {
			return fmt.Errorf("route suite controls name different packages: coverage %q, probe %q",
				coveragePackage, probePackage)
		}
	}
	return nil
}

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
		target := trace.MutationControlProbePrefix + record.Package
		if record.Package == "" {
			target = trace.MutationControlProbePrefix + "all"
		}
		if record.Target != target {
			return fmt.Errorf("exact original preflight target %q in package %q, want %q for that exact package",
				record.Target, record.Package, target)
		}
		if suiteRecorded || infectionsRecorded {
			return errors.New("exact original preflight carries suite or infected: a control is neither a routing suite nor a source of infection facts")
		}
	} else if strings.HasPrefix(record.Target, trace.MutationControlProbePrefix) {
		return fmt.Errorf("probe target %q has a mutation-control identity without control=true", record.Target)
	}
	if record.Suite {
		if !strings.HasPrefix(record.Target, trace.PackageSuiteProbePrefix) ||
			len(record.Target) == len(trace.PackageSuiteProbePrefix) || record.Package == "" ||
			record.Target != trace.PackageSuiteProbePrefix+record.Package {
			return fmt.Errorf("suite probe target %q in package %q, want the package-suite identity of that exact package",
				record.Target, record.Package)
		}
	} else if strings.HasPrefix(record.Target, trace.PackageSuiteProbePrefix) {
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

func missingField(name string) error {
	return fmt.Errorf("missing required field %q", name)
}

func checkNotEmpty(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	return nil
}

func checkNotNegative(name string, value int64) error {
	if value < 0 {
		return fmt.Errorf("%s is %d, which is below zero", name, value)
	}
	return nil
}

func checkTimestamp(value string) error {
	if err := checkNotEmpty("timestamp", value); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("timestamp %q is no RFC 3339 moment", value)
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
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
