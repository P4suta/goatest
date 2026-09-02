// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/goatest/internal/trace"
)

const (
	// readBufferSize is the buffer one line is assembled in. A line is not
	// bounded by it: a route event naming every target that reaches a mutant
	// can be far larger than any fixed buffer, so lines are read whole rather
	// than through a scanner that would refuse the long ones.
	readBufferSize = 1 << 16
	// outcomeKilled is the recorded outcome of an execution that killed its
	// mutant. It is the only outcome that proves a target reaches one: a
	// survived execution proves the target ran the mutant and nothing more.
	outcomeKilled = "killed"
	// runArgument is the flag a mutant execution selects its tests with. An
	// execution without one is the package suite.
	runArgument = "-test.run="
	// individualPlan and packageSuitePlan are the plan entries this audit
	// reads. A plan entry sits parallel to the reaching target it was derived
	// from, which is what maps a killer test back to the target that ran it;
	// the package suite names no target at all.
	individualPlan   = "individual:"
	packageSuitePlan = "package-suite"
)

// The reach layer and the reasons it gives. A why is written from the point of
// view of the evidence, because that is what a developer has to go and look at
// when the audit refuses a change.
const (
	reachLayerName          = "reach"
	whyNoProfile            = "the killer target left no coverage profile"
	whyCoversNoneOfTheFile  = "the killer target covers no block of the file"
	whyOutsideCoveredBlocks = "position outside every covered block of the killer"
)

// killPair is one kill a run recorded: the mutant, where the catalog placed
// it, and the target whose test killed it. It is the unit this tool preserves.
// A layer that would stop routing this mutant to this target would lose a kill
// the run proved, and no layer is allowed to do that.
type killPair struct {
	mutant  string
	display string
	rule    string
	path    string
	line    int
	column  int
	target  string
	killer  string
}

// pairKey identifies a kill pair. A run confirms a kill by repeating it, so
// one pair reaches the audit more than once and is audited once.
type pairKey struct {
	mutant string
	target string
}

// key is the identity of a pair.
func (pair killPair) key() pairKey { return pairKey{mutant: pair.mutant, target: pair.target} }

// conclusion is what a layer decided about one kill pair.
type conclusion int

const (
	// kept: the layer still routes the mutant to the target that killed it,
	// which is the only conclusion a sound layer reaches.
	kept conclusion = iota
	// discharged: the layer would drop the killer. The soundness invariant is
	// that this never happens, so it is reported as a violation.
	discharged
	// unverifiable: the recording does not carry the evidence the layer needs
	// to decide. It is listed rather than counted against the layer, because
	// an audit that guessed would be worth less than one that says it cannot
	// tell.
	unverifiable
)

// finding is what a layer concluded about one kill pair, and why.
type finding struct {
	conclusion conclusion
	why        string
}

// layer is one proof layer under audit: a name, and the rule it decides kill
// pairs by. Every layer that narrows what a mutant is run against has to keep
// every recorded killer, so every layer is audited the same way and reported
// in the same table. The next one — a branch-never-taken proof, which will
// need a mutant catalog beside the recording — is another value here.
type layer struct {
	name   string
	decide func(pair killPair, recorded evidence) finding
}

// auditLayers are the layers this tool audits, in the order it reports them.
func auditLayers() []layer { return []layer{reachLayer()} }

// reachLayer is the block routing of a mutation run: a mutant is run against
// the targets whose covered blocks contain its position, with the file as the
// fallback whenever the blocks cannot decide.
//
// The rule is reimplemented here from the recording and the profiles rather
// than called out of internal/assure. An audit that asked the code under audit
// whether it was right would prove only that the code agrees with itself; two
// independent implementations disagreeing is exactly the signal this tool
// exists to raise.
func reachLayer() layer { return layer{name: reachLayerName, decide: decideReach} }

// decideReach answers whether block routing keeps one recorded killer.
//
// The ladder is the routing ladder. A target that ran no block of the file is
// no candidate under any rule. A mutant the engine could not place cannot be
// narrowed to a block, so the file decides. A position inside a block the
// killer ran is reached. A position no instrumented block contains is a gap
// between the blocks cmd/cover cut rather than proof that nothing runs it, so
// the file decides again. What is left is a position the toolchain measured,
// inside the file the killer ran, and outside every block the killer executed:
// the killer would be dropped.
func decideReach(pair killPair, recorded evidence) finding {
	if !recorded.measured(pair.target) {
		return finding{conclusion: unverifiable, why: whyNoProfile}
	}
	covered, candidate := recorded.coveredBy(pair.target, pair.path)
	if !candidate {
		return finding{conclusion: discharged, why: whyCoversNoneOfTheFile}
	}
	if pair.line <= 0 || pair.column <= 0 {
		return finding{conclusion: kept}
	}
	if covered.Contains(pair.line, pair.column) {
		return finding{conclusion: kept}
	}
	if !recorded.instrumentedAt(pair.path, pair.line, pair.column) {
		return finding{conclusion: kept}
	}
	return finding{conclusion: discharged, why: whyOutsideCoveredBlocks}
}

// layerResult is what one layer concluded across the whole recording.
type layerResult struct {
	name         string
	audited      int
	kept         int
	unverifiable int
	violations   int
}

// auditRow is one kill pair a layer had something to say about.
type auditRow struct {
	pair  killPair
	layer string
	why   string
}

// auditResult is the whole audit of one recording: what the recording held,
// what each layer concluded, and the pairs worth naming one by one.
//
// The kills that name no single target are counted rather than dropped. A
// package suite settles a mutant no target reaches, a batch proves one of
// several targets killed a mutant without saying which, and a killer no route
// names is a recording whose halves disagree. None of the three is a pair to
// preserve, and all three are things a reader must be able to see the audit
// did not look at.
type auditResult struct {
	targets           int
	routes            int
	killedExecutions  int
	pairs             int
	packageSuiteKills int
	batchKills        int
	unattributedKills int
	truncatedLines    int
	layers            []layerResult
	unverifiable      []auditRow
	violations        []auditRow
}

// auditTrace replays one recording against the coverage a run left beside it
// and holds every layer to the kills the run proved.
//
// The stream is read in order, so the route in force for an execution is the
// last one recorded for its mutant: a run emits a mutant's route before it
// executes the mutant, and nothing else about the ordering is assumed.
//
// A last line the recording was cut in the middle of is tolerated and counted:
// that is what an interrupted run leaves, and refusing to audit the run that
// was interrupted would refuse the recordings most worth auditing. A broken
// line with a whole recording behind it is refused instead, because an audit
// that skipped an event it did not understand would be auditing less than it
// says it did.
//
// A line is decoded into the events this audit reads rather than validated
// against the whole trace contract, which internal/devtools/tracesummary
// exists to do. An audit is run against recordings older and newer than
// itself — that is what makes it an audit rather than a self-check — and a
// field a later version added is not a reason to refuse the kills the
// recording proves.
func auditTrace(source io.Reader, recorded evidence, layers []layer) (auditResult, error) {
	audit := newAuditor(recorded, layers)
	buffered := bufio.NewReaderSize(source, readBufferSize)
	for number := 1; ; number++ {
		line, readErr := buffered.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return auditResult{}, fmt.Errorf("line %d: %w", number, readErr)
		}
		ended := readErr != nil
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if ended {
				break
			}
			continue
		}
		var event trace.Event
		if err := json.Unmarshal(line, &event); err != nil {
			if ended {
				audit.result.truncatedLines++
				break
			}
			return auditResult{}, fmt.Errorf("line %d: %w", number, err)
		}
		audit.read(event)
		if ended {
			break
		}
	}
	return audit.finish(), nil
}

// auditor is one audit in progress: the evidence and the layers it holds the
// recording to, the routes it has read so far, and the pairs it has already
// decided.
type auditor struct {
	recorded evidence
	layers   []layer
	routes   map[string]trace.RouteRecord
	decided  map[pairKey]struct{}
	result   auditResult
}

func newAuditor(recorded evidence, layers []layer) *auditor {
	result := auditResult{targets: len(recorded.targets), layers: make([]layerResult, len(layers))}
	for index, applied := range layers {
		result.layers[index].name = applied.name
	}
	return &auditor{
		recorded: recorded, layers: layers,
		routes: make(map[string]trace.RouteRecord), decided: make(map[pairKey]struct{}), result: result,
	}
}

// read takes one event of the recording: a route is remembered, a kill is
// audited, and everything else is a part of the run this tool does not read.
func (audit *auditor) read(event trace.Event) {
	switch {
	case event.Type == trace.TypeRoute && event.Route != nil:
		audit.result.routes++
		audit.routes[event.Route.MutantID] = *event.Route
	case event.Type == trace.TypeMutantExec && event.Mutant != nil && event.Mutant.Outcome == outcomeKilled:
		audit.result.killedExecutions++
		audit.kill(*event.Mutant)
	}
}

// kill attributes one recorded kill to the target that ran it and holds every
// layer to the pair. A kill no single target can be attributed to is counted
// and left alone.
func (audit *auditor) kill(record trace.MutantRecord) {
	killers, selective := killerTests(record.Args)
	switch {
	case !selective:
		audit.result.packageSuiteKills++
		return
	case len(killers) > 1:
		audit.result.batchKills++
		return
	case len(killers) == 0:
		audit.result.unattributedKills++
		return
	}
	route, routed := audit.routes[record.ID]
	if !routed {
		audit.result.unattributedKills++
		return
	}
	target, named := routeTarget(route, killers[0])
	if !named {
		audit.result.unattributedKills++
		return
	}
	audit.decide(killPair{
		mutant: record.ID, display: record.DisplayID, rule: route.Rule, path: route.Path,
		line: route.Line, column: route.Column, target: target, killer: killers[0],
	})
}

// decide holds every layer to one kill pair, once. A run confirms a kill by
// repeating it, so the same pair arrives more than once and is decided the
// first time it does.
func (audit *auditor) decide(pair killPair) {
	if _, repeated := audit.decided[pair.key()]; repeated {
		return
	}
	audit.decided[pair.key()] = struct{}{}
	audit.result.pairs++
	for index, applied := range audit.layers {
		audit.result.layers[index].audited++
		concluded := applied.decide(pair, audit.recorded)
		row := auditRow{pair: pair, layer: applied.name, why: concluded.why}
		switch concluded.conclusion {
		case kept:
			audit.result.layers[index].kept++
		case unverifiable:
			audit.result.layers[index].unverifiable++
			audit.result.unverifiable = append(audit.result.unverifiable, row)
		case discharged:
			audit.result.layers[index].violations++
			audit.result.violations = append(audit.result.violations, row)
		}
	}
}

// finish orders what the audit found, so that one recording reports the same
// bytes however the run that made it was scheduled.
func (audit *auditor) finish() auditResult {
	slices.SortFunc(audit.result.unverifiable, compareRows)
	slices.SortFunc(audit.result.violations, compareRows)
	return audit.result
}

// killerTests reads the tests one mutant execution ran, and reports whether it
// selected any tests at all. An execution that selected none is the package
// suite; one that selected several is a batch, which proves that one of them
// killed the mutant without saying which.
func killerTests(arguments []string) ([]string, bool) {
	for _, argument := range arguments {
		pattern, selective := strings.CutPrefix(argument, runArgument)
		if !selective {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
		if group, grouped := strings.CutPrefix(pattern, "("); grouped {
			return strings.Split(strings.TrimSuffix(group, ")"), "|"), true
		}
		if pattern == "" {
			return nil, true
		}
		return []string{pattern}, true
	}
	return nil, false
}

// routeTarget maps a killer test back to the target that ran it. The plan of a
// route sits parallel to its reaching targets, so the entry that names the
// test names the target's position too.
func routeTarget(route trace.RouteRecord, test string) (string, bool) {
	for index, entry := range route.Plan {
		if entry == individualPlan+test && index < len(route.ReachingTargets) {
			return route.ReachingTargets[index], true
		}
	}
	return "", false
}

// compareRows orders the reported pairs by where their mutants are, so a
// report reads as a list of places to look at and renders the same bytes
// however the run was scheduled.
func compareRows(first, second auditRow) int {
	if order := strings.Compare(first.pair.path, second.pair.path); order != 0 {
		return order
	}
	if order := cmp.Compare(first.pair.line, second.pair.line); order != 0 {
		return order
	}
	if order := cmp.Compare(first.pair.column, second.pair.column); order != 0 {
		return order
	}
	if order := strings.Compare(first.pair.mutant, second.pair.mutant); order != 0 {
		return order
	}
	if order := strings.Compare(first.pair.target, second.pair.target); order != 0 {
		return order
	}
	return strings.Compare(first.layer, second.layer)
}
