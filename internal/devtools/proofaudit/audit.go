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
	"maps"
	"slices"
	"strings"

	goanalysis "github.com/P4suta/goatest/internal/golang"
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
	// coverageArgument is the flag a baseline measurement writes its target's
	// coverage profile with, and packageArgument the flag that names the
	// package the measured binary belongs to. A measurement's arguments are the
	// only place in a recording where a target's identity, the test it runs and
	// its package meet, which is what a kill is attributed through and what the
	// savings measurement reads.
	coverageArgument = "-test.coverprofile="
	packageArgument  = "-p"
	// fuzzPrefix is how Go names a fuzz target, and the whole of the rule that
	// says one is never discharged: fuzzing explores past the coverage its seed
	// corpus measured, so a body its profile never entered may still be entered.
	fuzzPrefix = "Fuzz"
)

// The layers and the reasons they give. A why is written from the point of
// view of the evidence, because that is what a developer has to go and look at
// when the audit refuses a change.
const (
	reachLayerName          = "reach"
	whyNoProfile            = "the killer target left no coverage profile"
	whyCoversNoneOfTheFile  = "the killer target covers no block of the file"
	whyOutsideCoveredBlocks = "position outside every covered block of the killer"

	branchLayerName   = "branch"
	whyNotInCatalog   = "the catalog does not list the mutant"
	whyBodyNeverTaken = "no covered block of the killer starts in the body the mutation gates"

	infectionLayerName    = "infection"
	whyNeverInfected      = "the killer target measured no infection by the mutant"
	whyProbeRecordedTwice = "the killer target has more than one probe record"
)

// probeFacts is what the probe pass measured of one target: the outcome it
// recorded, and, when it measured, the mutants it saw infect. A record that
// measured nothing carries no set at all rather than an empty one, so that
// "measured and infected nothing" and "never measured" cannot be confused.
//
// A target the recording holds two records for is marked conflicting instead:
// the two runs say different things about one target, and believing whichever
// arrived first would decide a pair on the order a run happened to write.
type probeFacts struct {
	outcome     string
	infected    map[string]struct{}
	conflicting bool
}

// measured reports whether the probe run of this target produced facts. Only a
// measured run does; every other outcome, and a run that errored before
// reaching one, says nothing about any mutant.
func (facts *probeFacts) measured() bool { return facts.outcome == trace.ProbeOutcomeMeasured }

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
	// probed is what the route in force said: the mutant carries a probe site,
	// so the probe pass could have measured it. probe is what that pass
	// recorded of the killer target, and is nil when it recorded nothing for
	// it. The two carry the whole of the infection facts, so that layer stays a
	// pure function of the pair like every other one.
	probed bool
	probe  *probeFacts
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
	// inapplicable: the layer has no proof about this mutant at all, so it
	// changes nothing about the pair. It is counted apart from kept because a
	// layer that proves almost nothing would otherwise report almost every
	// pair as one it keeps.
	inapplicable
)

// finding is what a layer concluded about one kill pair, and why.
type finding struct {
	conclusion conclusion
	why        string
}

// layer is one proof layer under audit: a name, and the rule it decides kill
// pairs by. Every layer that narrows what a mutant is run against has to keep
// every recorded killer, so every layer is audited the same way and reported
// in the same table.
type layer struct {
	name   string
	decide func(pair killPair, recorded evidence) finding
}

// auditLayers are the layers this tool audits, in the order the engine applies
// them and therefore the order it reports them.
//
// The branch layer decides by a proof only a catalog carries, so a run audited
// without one is a run that layer was never held to and it is left out of the
// audit entirely. Adding a row of zeroes instead would be worse than adding
// nothing: a reader skimming the table would take "not checked" for "checked
// and clean", which is the one misreading a soundness report may not invite.
//
// The infection layer decides by facts the recording itself carries, so there
// is no third input to gate it on and it is always appended. Whether the
// recording holds a probe pass to hold it to is only known once the whole
// recording was read, so that half of the same rule is applied in finish.
func auditLayers(catalog *mutantCatalog) []layer {
	layers := []layer{reachLayer()}
	if catalog != nil {
		layers = append(layers, branchLayer(catalog))
	}
	return append(layers, infectionLayer())
}

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

// branchLayer is the branch-never-taken discharge: go-mutants proves of some
// mutants that the mutated condition implies the original one, which makes the
// condition inert, so a test during which no statement of the body that
// condition gates ran cannot observe the mutation and never has to run it.
//
// The rule is reimplemented here from the catalog and the profiles for the
// reason the reach layer is: an audit that asked the code under audit whether
// it was right would prove only that the code agrees with itself.
func branchLayer(catalog *mutantCatalog) layer {
	return layer{
		name:   branchLayerName,
		decide: func(pair killPair, recorded evidence) finding { return decideBranch(catalog, pair, recorded) },
	}
}

// decideBranch answers whether the branch proof keeps one recorded killer.
//
// The ladder is the rule, and every rung of it that is not the last one keeps
// the killer. A mutant the catalog does not list is one the audit has no proof
// to apply; a mutant listed without a proof is one this layer changes nothing
// about; a proof whose span is not a body, or a body no profile instrumented,
// is a state the audit does not know the meaning of, and nothing is discharged
// from any of them. What is left is a killer whose profile says it ran the file
// and started no block inside the body the mutation gates, which is the target
// the layer would drop.
//
// The body is found by where a block starts and never by what it contains:
// cmd/cover records the body's first block starting at the opening brace on one
// toolchain and at the first statement on another, and only the start is inside
// the span under both.
//
// A fuzz target is held to the proof like any other killer, although the engine
// never discharges one. The exemption guards the fuzzing pass, which explores
// past the coverage the seed corpus measured — and that pass selects no test,
// so its kills are never a pair. A kill attributed to a fuzz target is its seed
// corpus killing the mutant, and a seed corpus that never entered the body
// under measurement cannot have entered it under an inert condition either: if
// it killed anyway, the proof was wrong, and that is the violation to print.
func decideBranch(catalog *mutantCatalog, pair killPair, recorded evidence) finding {
	listed, known := catalog.lookup(pair.mutant)
	if !known {
		return finding{conclusion: unverifiable, why: whyNotInCatalog}
	}
	if listed.Branch == nil {
		return finding{conclusion: inapplicable}
	}
	body, proved := listed.proves()
	if !proved {
		return finding{conclusion: kept}
	}
	if !startsInBody(recorded.instrumentedIn(listed.Path), body) {
		return finding{conclusion: kept}
	}
	if !recorded.measured(pair.target) {
		return finding{conclusion: unverifiable, why: whyNoProfile}
	}
	covered, _ := recorded.coveredBy(pair.target, listed.Path)
	if startsInBody(covered, body) {
		return finding{conclusion: kept}
	}
	return finding{conclusion: discharged, why: whyBodyNeverTaken}
}

// startsInBody reports whether any block of one file starts inside the body.
// The blocks are walked here rather than asked of internal/golang, because
// containment is the question the reach layer asks and this layer asks a
// different one of the same evidence.
func startsInBody(file goanalysis.FileCoverage, body branchProof) bool {
	for _, block := range file.Blocks {
		if body.holds(block.StartLine, block.StartColumn) {
			return true
		}
	}
	return false
}

// infectionLayer is the infection discharge: a probe pass runs every target
// once against a tree in which each eligible mutant site records whether the
// value the original computed ever differed from the constant the mutant would
// put there. A target whose probe run was measured and never saw a mutant's
// site differ cannot observe that mutation by running the same test, so routing
// may drop that target for that mutant.
//
// The rule is reimplemented here from the recording for the reason the other
// two layers are: an audit that asked the code under audit whether it was right
// would prove only that the code agrees with itself.
func infectionLayer() layer { return layer{name: infectionLayerName, decide: decideInfection} }

// decideInfection answers whether the infection facts keep one recorded killer.
//
// The ladder is the rule, and every rung of it but the last keeps the killer. A
// mutant no probe site was compiled for is one this layer has nothing to say
// about; a killer the pass recorded nothing for, and a record that measured
// nothing — its test failed, it timed out, the tree was unavailable, or it
// errored before any outcome — are all recordings with no facts in them; a
// killer with two records is a recording whose meaning the audit does not know.
// What is left is a probed mutant whose killer was measured and whose measured
// killer did not name it, which is the target the layer would drop.
//
// There is no fuzz exemption here, unlike the branch layer. The probe pass
// records nothing for a fuzz target, because fuzzing explores past the seed
// corpus the coverage was measured from, so a fuzz killer lands on the rung
// that keeps every killer with no record at all. A record that does exist is
// held to whatever it says.
//
// The coverage evidence is not read: the pair carries every fact this rule
// decides by.
func decideInfection(pair killPair, _ evidence) finding {
	if !pair.probed {
		return finding{conclusion: inapplicable}
	}
	if pair.probe == nil {
		return finding{conclusion: kept}
	}
	if pair.probe.conflicting {
		return finding{conclusion: unverifiable, why: whyProbeRecordedTwice}
	}
	if !pair.probe.measured() {
		return finding{conclusion: kept}
	}
	if _, infected := pair.probe.infected[pair.mutant]; infected {
		return finding{conclusion: kept}
	}
	return finding{conclusion: discharged, why: whyNeverInfected}
}

// layerResult is what one layer concluded across the whole recording.
type layerResult struct {
	name         string
	audited      int
	kept         int
	inapplicable int
	unverifiable int
	violations   int
}

// dischargeSavings is what a layer would have bought on the recording, which is
// the other half of the question this tool answers: soundness says the layer
// may be used, and this says whether it is worth using.
//
// It is measured from the routes and the recorded evidence rather than read off
// the discharged targets of the trace, because the recordings worth auditing
// are the ones made by runs that discharged nothing — a run that already
// applied the layer cannot say what applying it would have saved.
type dischargeSavings struct {
	// routes is how many routed mutants carry evidence the rule may act on.
	routes int
	// reaching and discharged are the targets those routes reach, and the ones
	// the rule would have dropped.
	reaching   int
	discharged int
	// emptied is the routes the rule would leave with no reaching target at
	// all, which is a mutant that would stop being executed rather than one
	// executed less.
	emptied int
	// executions is the recorded mutant executions that would not have
	// happened.
	executions int
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
	targets int
	routes  int
	// reusedRoutes counts the routes whose verdict the run took from an
	// earlier run's evidence. Nothing was executed for one, so it is neither a
	// kill this audit can hold to a layer nor a mutant the recording lost: it
	// is a class of its own, and the audited share of a run is read against
	// the part of the run that ran.
	reusedRoutes      int
	killedExecutions  int
	pairs             int
	packageSuiteKills int
	batchKills        int
	unattributedKills int
	truncatedLines    int
	// probeExecutions is how many targets the probe pass executed, and
	// probeMeasured how many distinct targets it got facts out of. The two say
	// how much of the run the infection facts cover, which is what the layer's
	// numbers have to be read against.
	probeExecutions int
	probeMeasured   int
	layers          []layerResult
	unverifiable    []auditRow
	violations      []auditRow
	// branchAudited says whether a catalog was given, and infectionAudited
	// whether the recording held a probe pass, with branch and infection what
	// each layer would have bought when it was audited. A layer nobody held the
	// run to measures nothing, and says so rather than printing zeroes.
	branchAudited    bool
	infectionAudited bool
	branch           dischargeSavings
	infection        dischargeSavings
}

// targetIdentity is a test and the package it runs in. It is what a baseline
// measurement says about a target, and equally what a recorded mutant execution
// that selected a single test says about itself — which is exactly why the two
// can be matched: an execution would not have happened when the target it names
// is one the rule discharges, and a test name is only unique within a package.
type targetIdentity struct {
	test        string
	packagePath string
}

// auditTrace replays one recording against the coverage a run left beside it
// and holds every layer to the kills the run proved.
//
// The stream is read in order, so the route in force for an execution is the
// last one recorded for its mutant: a run emits a mutant's route before it
// executes the mutant, and nothing else about the ordering is assumed. The
// probe facts in force are read the same way: a run records its whole probe
// pass, which is one phase of its own, before it routes or executes any mutant,
// so every probe record is in hand by the time a kill is decided. A record that
// arrived after a kill of its target is simply not seen by that pair, which is
// the rung of the ladder that keeps the killer and never a false violation.
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
// The catalog is given beside the layers because the audit reads it twice
// over: once per kill pair, through the layer that decides by it, and once over
// the whole recording, to measure what that layer would have saved. The layers
// alone would answer the first question and not the second.
func auditTrace(source io.Reader, recorded evidence, catalog *mutantCatalog, layers []layer) (auditResult, error) {
	audit := newAuditor(recorded, catalog, layers)
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

// auditor is one audit in progress: the evidence, the catalog and the layers it
// holds the recording to, what it has read of the recording so far, and the
// pairs it has already decided.
//
// The targets are read from the baseline measurements two ways round, because
// the audit asks both questions: which test a target ran, and which target ran
// a test. The probes are what the probe pass recorded of each target. The
// executions are collected for the savings measurements alone, and both of them
// read them, so they are collected whatever inputs the audit was given.
type auditor struct {
	recorded   evidence
	catalog    *mutantCatalog
	layers     []layer
	routes     map[string]trace.RouteRecord
	targets    map[string]targetIdentity
	measuredBy map[targetIdentity]string
	probes     map[string]*probeFacts
	executions map[string][]targetIdentity
	decided    map[pairKey]struct{}
	result     auditResult
}

func newAuditor(recorded evidence, catalog *mutantCatalog, layers []layer) *auditor {
	result := auditResult{targets: len(recorded.targets), layers: make([]layerResult, len(layers))}
	for index, applied := range layers {
		result.layers[index].name = applied.name
		result.branchAudited = result.branchAudited || applied.name == branchLayerName
		result.infectionAudited = result.infectionAudited || applied.name == infectionLayerName
	}
	return &auditor{
		recorded: recorded, catalog: catalog, layers: layers,
		routes: make(map[string]trace.RouteRecord), targets: make(map[string]targetIdentity),
		measuredBy: make(map[targetIdentity]string), probes: make(map[string]*probeFacts),
		executions: make(map[string][]targetIdentity),
		decided:    make(map[pairKey]struct{}), result: result,
	}
}

// read takes one event of the recording: a route is remembered, a baseline
// measurement names a target, a probe execution says what the pass measured of
// one, an execution is collected and a kill among them audited, and everything
// else is a part of the run this tool does not read.
func (audit *auditor) read(event trace.Event) {
	switch {
	case event.Type == trace.TypeExec && event.Exec != nil:
		audit.measurement(event.Exec.Argv)
	case event.Type == trace.TypeRoute && event.Route != nil:
		audit.result.routes++
		if event.Route.Reused {
			audit.result.reusedRoutes++
		}
		audit.routes[event.Route.MutantID] = *event.Route
	case event.Type == trace.TypeProbeExec && event.Probe != nil:
		audit.probe(*event.Probe)
	case event.Type == trace.TypeMutantExec && event.Mutant != nil:
		audit.execution(*event.Mutant)
	}
}

// probe takes one probe execution: what the pass measured of one target. The
// infections are read into a set because the recorded list is in catalogue
// order, which is not the order the identities sort in, so it can only be
// searched by looking at all of it.
//
// A target the recording names twice is a target the audit does not know the
// facts of: two runs of one target say different things, and believing
// whichever came first would decide every pair of that killer on the order a
// run happened to write. Both records are dropped for a mark that says so.
func (audit *auditor) probe(record trace.ProbeRecord) {
	audit.result.probeExecutions++
	if _, recorded := audit.probes[record.Target]; recorded {
		audit.probes[record.Target] = &probeFacts{conflicting: true}
		return
	}
	facts := &probeFacts{outcome: record.Outcome}
	if facts.measured() {
		facts.infected = make(map[string]struct{}, len(record.Infected))
		for _, mutant := range record.Infected {
			facts.infected[mutant] = struct{}{}
		}
	}
	audit.probes[record.Target] = facts
}

// measurement reads a target's identity out of one recorded command. A baseline
// measurement writes a target's coverage profile under the target's own
// identity while selecting the target's single test in the target's package,
// which is the only place a recording puts the three together. Every other
// command a run executes names no profile and is passed over.
//
// Two targets claiming one identity would identify neither, so a name a second
// target claims is unclaimed rather than given to whichever measurement came
// first: a kill the audit cannot place is worth more than a kill it places
// wrongly.
func (audit *auditor) measurement(argv []string) {
	target, identity := "", targetIdentity{}
	for index, argument := range argv {
		switch {
		case strings.HasPrefix(argument, coverageArgument):
			target = profileTarget(strings.TrimPrefix(argument, coverageArgument))
		case argument == packageArgument && index+1 < len(argv):
			identity.packagePath = argv[index+1]
		}
	}
	if selected, selective := killerTests(argv); selective && len(selected) == 1 {
		identity.test = selected[0]
	}
	if target == "" {
		return
	}
	audit.targets[target] = identity
	if identity.test == "" {
		return
	}
	if claimed, measured := audit.measuredBy[identity]; measured && claimed != target {
		audit.measuredBy[identity] = ""
		return
	}
	audit.measuredBy[identity] = target
}

// measuredTarget is the target whose baseline measurement ran one test in one
// package, and whether the recording named exactly one.
func (audit *auditor) measuredTarget(packagePath, test string) (string, bool) {
	target, measured := audit.measuredBy[targetIdentity{test: test, packagePath: packagePath}]
	return target, measured && target != ""
}

// execution takes one recorded mutant execution: it is collected for the
// savings measurements, which both read the executions a layer would have
// removed, and audited when it killed.
func (audit *auditor) execution(record trace.MutantRecord) {
	if selected, selective := killerTests(record.Args); selective && len(selected) == 1 {
		audit.executions[record.ID] = append(audit.executions[record.ID],
			targetIdentity{test: selected[0], packagePath: record.Package})
	}
	if record.Outcome != outcomeKilled {
		return
	}
	audit.result.killedExecutions++
	audit.kill(record)
}

// profileTarget is the target a coverage profile path names. A run writes one
// profile per target under the target's identity, and a recording made on
// another operating system names it with that system's separator.
func profileTarget(path string) string {
	if cut := strings.LastIndexAny(path, `/\`); cut >= 0 {
		path = path[cut+1:]
	}
	target, named := strings.CutSuffix(path, profileSuffix)
	if !named {
		return ""
	}
	return target
}

// kill attributes one recorded kill to the target that ran it and holds every
// layer to the pair. A kill no single target can be attributed to is counted
// and left alone.
//
// The target is the one whose baseline measurement ran the killer test in the
// package the execution ran in, and not the target sitting at the position of
// the matching plan entry. A plan holds one entry per execution rather than one
// per reaching target: the targets a run executes one at a time come first, the
// rest are batched, and a batch of a single target is rendered exactly like an
// individual run. Past the individual prefix the two lists no longer line up,
// so a plan position is a coincidence and never an identity. A measurement is,
// because it is the run saying which target ran which test where.
//
// The route is still what says where the mutant is and which rule made it,
// which is why a kill recorded before any route is one the audit cannot place.
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
	target, measured := audit.measuredTarget(record.Package, killers[0])
	if !measured {
		audit.result.unattributedKills++
		return
	}
	audit.decide(killPair{
		mutant: record.ID, display: record.DisplayID, rule: route.Rule, path: route.Path,
		line: route.Line, column: route.Column, target: target, killer: killers[0],
		probed: route.Probed, probe: audit.probes[target],
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
		case inapplicable:
			audit.result.layers[index].inapplicable++
		case unverifiable:
			audit.result.layers[index].unverifiable++
			audit.result.unverifiable = append(audit.result.unverifiable, row)
		case discharged:
			audit.result.layers[index].violations++
			audit.result.violations = append(audit.result.violations, row)
		}
	}
}

// finish measures what each layer would have saved and orders what the audit
// found, so that one recording reports the same bytes however the run that made
// it was scheduled.
//
// A recording holding no probe pass is a recording the infection layer was
// never held to, and only the whole of it says so, so the row that would have
// read as a clean one is removed here rather than never added. Nothing is lost
// with it: without a single probe record every pair reached a rung of the
// ladder that keeps the killer or does not apply to it, so neither list of
// named pairs carries a row of that layer's.
func (audit *auditor) finish() auditResult {
	for _, facts := range audit.probes {
		if facts.measured() {
			audit.result.probeMeasured++
		}
	}
	audit.result.infectionAudited = audit.result.infectionAudited && audit.result.probeExecutions > 0
	if !audit.result.infectionAudited {
		audit.result.layers = slices.DeleteFunc(audit.result.layers,
			func(audited layerResult) bool { return audited.name == infectionLayerName })
	}
	audit.result.branch = audit.measureBranchSavings()
	audit.result.infection = audit.measureInfectionSavings()
	slices.SortFunc(audit.result.unverifiable, compareRows)
	slices.SortFunc(audit.result.violations, compareRows)
	return audit.result
}

// measureBranchSavings counts what the branch layer would have removed from the
// run that was recorded.
//
// Only a route decided by block and not fallen back to the file is counted:
// those are the routes the layer would have narrowed, and a route the engine
// decided by the file is one whose reaching set the proof was never asked
// about. The mutants are walked in identity order so the totals are read off
// the recording rather than off the order a map happened to be built in.
func (audit *auditor) measureBranchSavings() dischargeSavings {
	if audit.catalog == nil {
		return dischargeSavings{}
	}
	var measured dischargeSavings
	for _, mutant := range slices.Sorted(maps.Keys(audit.routes)) {
		route := audit.routes[mutant]
		listed, known := audit.catalog.lookup(mutant)
		if !known || route.Granularity != trace.GranularityBlock || route.Fallback != "" {
			continue
		}
		body, proved := listed.proves()
		if !proved || !startsInBody(audit.recorded.instrumentedIn(listed.Path), body) {
			continue
		}
		measured.routes++
		measured.reaching += len(route.ReachingTargets)
		discharged := 0
		dropped := make(map[targetIdentity]struct{}, len(route.ReachingTargets))
		for _, target := range route.ReachingTargets {
			if !audit.discharges(listed.Path, body, target) {
				continue
			}
			discharged++
			dropped[audit.targets[target]] = struct{}{}
		}
		measured.discharged += discharged
		if discharged > 0 && discharged == len(route.ReachingTargets) {
			measured.emptied++
		}
		for _, executed := range audit.executions[mutant] {
			if _, saved := dropped[executed]; saved {
				measured.executions++
			}
		}
	}
	return measured
}

// measureInfectionSavings counts what the infection layer would have removed
// from the run that was recorded.
//
// Every route of a probed mutant is counted, whatever decided it: a route
// fallen back to the file, or one of file granularity, reaches its targets by a
// rule the probe facts are independent of, and the layer narrows the reaching
// set of all three the same way. That is the difference from the branch
// measurement, which counts block routes alone because the proof it applies is
// about the block the mutation sits in.
//
// The mutants are walked in identity order so the totals are read off the
// recording rather than off the order a map happened to be built in.
//
// A run that already applied the layer measures nothing here, by construction:
// the targets it discharged left reaching_targets, so what is counted is what
// the rule would still drop, which on such a route is nothing. That is the
// measurement working, not failing — the number to read on those recordings is
// the discharged targets of the trace, and the number this reports is what a
// recording made without the layer would have bought.
func (audit *auditor) measureInfectionSavings() dischargeSavings {
	if !audit.result.infectionAudited {
		return dischargeSavings{}
	}
	var measured dischargeSavings
	for _, mutant := range slices.Sorted(maps.Keys(audit.routes)) {
		route := audit.routes[mutant]
		if !route.Probed {
			continue
		}
		measured.routes++
		measured.reaching += len(route.ReachingTargets)
		discharged := 0
		dropped := make(map[targetIdentity]struct{}, len(route.ReachingTargets))
		for _, target := range route.ReachingTargets {
			if !audit.neverInfected(mutant, target) {
				continue
			}
			discharged++
			dropped[audit.targets[target]] = struct{}{}
		}
		measured.discharged += discharged
		if discharged > 0 && discharged == len(route.ReachingTargets) {
			measured.emptied++
		}
		for _, executed := range audit.executions[mutant] {
			if _, saved := dropped[executed]; saved {
				measured.executions++
			}
		}
	}
	return measured
}

// neverInfected reports whether the rule would drop one reaching target of a
// probed mutant. A target the pass recorded nothing for, one it recorded twice,
// and one whose run measured nothing are all targets with no facts against
// them, so what is left is dropped exactly when the measured run did not name
// the mutant.
func (audit *auditor) neverInfected(mutant, target string) bool {
	facts, recorded := audit.probes[target]
	if !recorded || facts.conflicting || !facts.measured() {
		return false
	}
	_, infected := facts.infected[mutant]
	return !infected
}

// discharges reports whether the rule would drop one reaching target of a
// proved mutant. A fuzz target is never dropped, a target the run left no
// profile for is never dropped, and what is left is dropped exactly when it
// started no block inside the body the mutation gates.
func (audit *auditor) discharges(path string, body branchProof, target string) bool {
	if strings.HasPrefix(audit.targets[target].test, fuzzPrefix) {
		return false
	}
	if !audit.recorded.measured(target) {
		return false
	}
	covered, _ := audit.recorded.coveredBy(target, path)
	return !startsInBody(covered, body)
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
