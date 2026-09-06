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
	"path/filepath"
	"slices"
	"strings"

	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/trace"
)

const (
	readBufferSize = 1 << 16

	outcomeKilled = "killed"

	runArgument = "-test.run="

	coverageArgument = "-test.coverprofile="

	minimumCompiledTestCommandArguments = 5
	goTestSubcommandIndex               = 1
	goTestFirstFlagIndex                = 2
)

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

	suiteReachLayerName        = "suite-reach"
	whyNoSuiteProfile          = "the package suite left no attributable coverage profile"
	whyConflictingSuiteProfile = "the package suite has more than one coverage profile identity"
	whyNoSuiteRoute            = "the package-suite kill has no preceding mutant route"
)

type probeFacts struct {
	outcome     string
	infected    map[string]struct{}
	conflicting bool
}

func (facts *probeFacts) measured() bool { return facts.outcome == trace.ProbeOutcomeMeasured }

type killPair struct {
	mutant  string
	display string
	rule    string
	path    string
	line    int
	column  int
	target  string
	killer  string

	granularity string

	evidenceTarget string

	probed bool
	probe  *probeFacts
}

type pairKey struct {
	mutant string
	target string
}

func (pair killPair) key() pairKey { return pairKey{mutant: pair.mutant, target: pair.target} }

func (pair killPair) coverageTarget() string {
	if pair.evidenceTarget != "" {
		return pair.evidenceTarget
	}
	return pair.target
}

type conclusion int

const (
	kept conclusion = iota

	discharged

	unverifiable

	inapplicable
)

type finding struct {
	conclusion conclusion
	why        string
}

type layer struct {
	name   string
	decide func(pair killPair, recorded evidence) finding
}

func auditLayers(catalog *mutantCatalog) []layer {
	layers := []layer{reachLayer()}
	if catalog != nil {
		layers = append(layers, branchLayer(catalog))
	}
	return append(layers, infectionLayer())
}

func reachLayer() layer { return layer{name: reachLayerName, decide: decideReach} }

func decideReach(pair killPair, recorded evidence) finding {
	target := pair.coverageTarget()
	if !recorded.measured(target) {
		return finding{conclusion: unverifiable, why: whyNoProfile}
	}
	covered, candidate := recorded.coveredBy(target, pair.path)
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

func decideSuiteReach(pair killPair, recorded evidence) finding {
	target := pair.coverageTarget()
	if !recorded.measured(target) {
		return finding{conclusion: unverifiable, why: whyNoProfile}
	}
	if pair.line <= 0 || pair.column <= 0 {
		return finding{conclusion: kept}
	}
	if !recorded.instrumentedBy(target, pair.path).Contains(pair.line, pair.column) {
		return finding{conclusion: kept}
	}
	covered, _ := recorded.coveredBy(target, pair.path)
	if covered.Contains(pair.line, pair.column) {
		return finding{conclusion: kept}
	}
	return finding{conclusion: discharged, why: whyOutsideCoveredBlocks}
}

func branchLayer(catalog *mutantCatalog) layer {
	return layer{
		name:   branchLayerName,
		decide: func(pair killPair, recorded evidence) finding { return decideBranch(catalog, pair, recorded) },
	}
}

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

func startsInBody(file goanalysis.FileCoverage, body branchProof) bool {
	for _, block := range file.Blocks {
		if body.holds(block.StartLine, block.StartColumn) {
			return true
		}
	}
	return false
}

func infectionLayer() layer { return layer{name: infectionLayerName, decide: decideInfection} }

func decideInfection(pair killPair, _ evidence) finding {
	if pair.granularity != "" && pair.granularity != trace.GranularityBlock {
		return finding{conclusion: inapplicable}
	}
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

type layerResult struct {
	name         string
	audited      int
	kept         int
	inapplicable int
	unverifiable int
	violations   int
}

type dischargeSavings struct {
	routes int

	reaching   int
	discharged int

	emptied int

	executions int
}

type auditRow struct {
	pair  killPair
	layer string
	why   string
}

type auditResult struct {
	targets int
	routes  int

	reusedRoutes      int
	killedExecutions  int
	pairs             int
	packageSuiteKills int
	batchKills        int
	unattributedKills int
	truncatedLines    int

	probeExecutions int
	probeMeasured   int

	suiteProbeExecutions  int
	suiteProbeMeasured    int
	suiteCoverageProfiles int
	suitePairs            int
	suiteReach            layerResult
	layers                []layerResult
	unverifiable          []auditRow
	violations            []auditRow

	branchAudited    bool
	infectionAudited bool
	branch           dischargeSavings
	infection        dischargeSavings
}

type targetIdentity struct {
	test        string
	packagePath string
}

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
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			if ended && errors.Is(err, io.ErrUnexpectedEOF) {
				audit.result.truncatedLines++
				break
			}
			return auditResult{}, fmt.Errorf("line %d: %w", number, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return auditResult{}, fmt.Errorf("line %d has trailing data", number)
		}
		audit.read(event)
		if ended {
			break
		}
	}
	return audit.finish(), nil
}

type auditor struct {
	recorded              evidence
	catalog               *mutantCatalog
	layers                []layer
	routes                map[string]trace.RouteRecord
	targets               map[string]targetIdentity
	measuredBy            map[targetIdentity]string
	probes                map[string]*probeFacts
	executions            map[string][]targetIdentity
	testBinaries          map[string]string
	testBinaryConflicts   map[string]bool
	suiteProfiles         map[string]string
	suiteProfileConflicts map[string]bool
	measuredSuiteProbes   map[string]struct{}
	decided               map[pairKey]struct{}
	suiteDecided          map[pairKey]struct{}
	result                auditResult
}

func newAuditor(recorded evidence, catalog *mutantCatalog, layers []layer) *auditor {
	targetProfiles, suiteProfiles := recorded.profileCounts()
	result := auditResult{
		targets: targetProfiles, suiteCoverageProfiles: suiteProfiles,
		layers: make([]layerResult, len(layers)),
	}
	for index, applied := range layers {
		result.layers[index].name = applied.name
		result.branchAudited = result.branchAudited || applied.name == branchLayerName
		result.infectionAudited = result.infectionAudited || applied.name == infectionLayerName
	}
	return &auditor{
		recorded: recorded, catalog: catalog, layers: layers,
		routes: make(map[string]trace.RouteRecord), targets: make(map[string]targetIdentity),
		measuredBy: make(map[targetIdentity]string), probes: make(map[string]*probeFacts),
		executions: make(map[string][]targetIdentity), testBinaries: make(map[string]string),
		testBinaryConflicts: make(map[string]bool), suiteProfiles: make(map[string]string),
		suiteProfileConflicts: make(map[string]bool),
		measuredSuiteProbes:   make(map[string]struct{}),
		decided:               make(map[pairKey]struct{}), suiteDecided: make(map[pairKey]struct{}), result: result,
	}
}

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

func (audit *auditor) probe(record trace.ProbeRecord) {
	if record.Control {
		return
	}
	if record.Suite {
		audit.result.suiteProbeExecutions++
		if record.Outcome == trace.ProbeOutcomeMeasured {
			if _, measured := audit.measuredSuiteProbes[record.Target]; !measured {
				audit.measuredSuiteProbes[record.Target] = struct{}{}
				audit.result.suiteProbeMeasured++
			}
		}
		return
	}
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

func (audit *auditor) measurement(argv []string) {
	if binary, packagePath, compiled := compiledTestBinary(argv); compiled {
		if previous, exists := audit.testBinaries[binary]; exists && previous != packagePath {
			audit.testBinaries[binary] = ""
			audit.testBinaryConflicts[binary] = true
		} else if !audit.testBinaryConflicts[binary] {
			audit.testBinaries[binary] = packagePath
		}
		return
	}
	target, identity := "", targetIdentity{}
	for _, argument := range argv {
		switch {
		case strings.HasPrefix(argument, coverageArgument):
			target = profileTarget(strings.TrimPrefix(argument, coverageArgument))
		}
	}
	if identity.packagePath == "" && len(argv) != 0 && !audit.testBinaryConflicts[argv[0]] {
		identity.packagePath = audit.testBinaries[argv[0]]
	}
	if selected, selective := killerTests(argv); selective && len(selected) == 1 {
		identity.test = selected[0]
	}
	if target == "" {
		return
	}
	audit.targets[target] = identity
	if identity.test == "" {
		if strings.HasSuffix(target, ".suite") && identity.packagePath != "" {
			if previous, exists := audit.suiteProfiles[identity.packagePath]; exists && previous != target {
				audit.suiteProfiles[identity.packagePath] = ""
				audit.suiteProfileConflicts[identity.packagePath] = true
			} else if !audit.suiteProfileConflicts[identity.packagePath] {
				audit.suiteProfiles[identity.packagePath] = target
			}
		}
		return
	}
	if claimed, measured := audit.measuredBy[identity]; measured && claimed != target {
		audit.measuredBy[identity] = ""
		return
	}
	audit.measuredBy[identity] = target
}

func compiledTestBinary(argv []string) (string, string, bool) {
	if len(argv) < minimumCompiledTestCommandArguments || !goCommandName(argv[0]) || argv[goTestSubcommandIndex] != "test" {
		return "", "", false
	}
	compiled, output, outputNext := false, "", false
	for _, argument := range argv[goTestFirstFlagIndex : len(argv)-1] {
		if outputNext {
			output = argument
			outputNext = false
			continue
		}
		switch {
		case argument == "-c":
			compiled = true
		case argument == "-o":
			output = ""
			outputNext = true
		case strings.HasPrefix(argument, "-o="):
			output = strings.TrimPrefix(argument, "-o=")
		}
	}
	packagePath := argv[len(argv)-1]
	if !compiled || output == "" || packagePath == "" || strings.HasPrefix(packagePath, "-") {
		return "", "", false
	}
	return output, packagePath, true
}

func goCommandName(name string) bool {
	if cut := strings.LastIndexAny(name, `/\\`); cut >= 0 {
		name = name[cut+1:]
	} else {
		name = filepath.Base(name)
	}
	return name == "go" || name == "go.exe"
}

func (audit *auditor) measuredTarget(packagePath, test string) (string, bool) {
	target, measured := audit.measuredBy[targetIdentity{test: test, packagePath: packagePath}]
	return target, measured && target != ""
}

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

func (audit *auditor) kill(record trace.MutantRecord) {
	killers, selective := killerTests(record.Args)
	switch {
	case !selective:
		audit.result.packageSuiteKills++
		audit.suiteKill(record)
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
		granularity: route.Granularity, probed: route.Probed, probe: audit.probes[target],
	})
}

func (audit *auditor) suiteKill(record trace.MutantRecord) {
	target, profiled := audit.suiteProfiles[record.Package]
	conflicting := audit.suiteProfileConflicts[record.Package]
	if audit.result.suiteCoverageProfiles == 0 {
		return
	}
	pair := killPair{
		mutant: record.ID, display: record.DisplayID,
		target: record.Package + " package suite", evidenceTarget: target,
		killer: record.Package + " package suite",
	}
	if _, repeated := audit.suiteDecided[pair.key()]; repeated {
		return
	}
	audit.suiteDecided[pair.key()] = struct{}{}
	audit.result.suitePairs++
	audit.result.suiteReach.audited++
	route, routed := audit.routes[record.ID]
	var concluded finding
	switch {
	case conflicting:
		concluded = finding{conclusion: unverifiable, why: whyConflictingSuiteProfile}
	case !profiled:
		concluded = finding{conclusion: unverifiable, why: whyNoSuiteProfile}
	case !routed:
		concluded = finding{conclusion: unverifiable, why: whyNoSuiteRoute}
	default:
		pair.rule, pair.path, pair.line, pair.column = route.Rule, route.Path, route.Line, route.Column
		concluded = decideSuiteReach(pair, audit.recorded)
	}
	row := auditRow{pair: pair, layer: suiteReachLayerName, why: concluded.why}
	switch concluded.conclusion {
	case kept:
		audit.result.suiteReach.kept++
	case inapplicable:
		audit.result.suiteReach.inapplicable++
	case unverifiable:
		audit.result.suiteReach.unverifiable++
		audit.result.unverifiable = append(audit.result.unverifiable, row)
	case discharged:
		audit.result.suiteReach.violations++
		audit.result.violations = append(audit.result.violations, row)
	}
}

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
	if audit.result.suiteCoverageProfiles != 0 {
		audit.result.suiteReach.name = suiteReachLayerName
		audit.result.layers = append(audit.result.layers, audit.result.suiteReach)
	}
	slices.SortFunc(audit.result.unverifiable, compareRows)
	slices.SortFunc(audit.result.violations, compareRows)
	return audit.result
}

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

func (audit *auditor) measureInfectionSavings() dischargeSavings {
	if !audit.result.infectionAudited {
		return dischargeSavings{}
	}
	var measured dischargeSavings
	for _, mutant := range slices.Sorted(maps.Keys(audit.routes)) {
		route := audit.routes[mutant]
		if !route.Probed || route.Granularity != trace.GranularityBlock || route.Fallback != "" {
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

func (audit *auditor) neverInfected(mutant, target string) bool {
	facts, recorded := audit.probes[target]
	if !recorded || facts.conflicting || !facts.measured() {
		return false
	}
	_, infected := facts.infected[mutant]
	return !infected
}

func (audit *auditor) discharges(path string, body branchProof, target string) bool {
	if !audit.recorded.measured(target) {
		return false
	}
	covered, _ := audit.recorded.coveredBy(target, path)
	return !startsInBody(covered, body)
}

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
