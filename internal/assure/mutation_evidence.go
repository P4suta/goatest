// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/evidence"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/report"
)

const mutationEvidenceFileName = evidence.MutationFileName

var moduleManifestFiles = []string{"go.mod", "go.sum"}

type targetIdentity struct {
	pkg  string
	name string
	kind string
}

func identify(target goanalysis.Target) targetIdentity {
	return targetIdentity{pkg: target.Package, name: target.Name, kind: string(target.Kind)}
}

func mutationEvidenceGuarded(round int, loaded config.Config, options Options) bool {
	return round == 0 && len(loaded.Resources) == 0 &&
		!options.Changed && !options.PackageScope && defaultPackagePatterns(options.Packages) &&
		options.ReplayMutantID == "" && options.ReplayFindingID == ""
}

type targetKeySources struct {
	inputs         evidence.Inputs
	model          goanalysis.Model
	contract       string
	testArgs       []string
	buildTags      []string
	commandTimeout time.Duration
	targetTimeout  time.Duration

	repositoryReaders map[string]bool

	extraFiles []string

	packages  map[string]goanalysis.Package
	directory map[string][]string
	testdata  map[string][]string
	corpus    map[string][]string
}

func newTargetKeySources(inputs evidence.Inputs, model goanalysis.Model, contract string, options Options, readers map[string]bool) targetKeySources {
	sources := targetKeySources{
		inputs: inputs, model: model, contract: contract,
		testArgs: options.TestArgs, buildTags: options.BuildTags,
		commandTimeout: options.CommandTimeout, targetTimeout: options.TargetTimeout,
		repositoryReaders: readers,
		packages:          make(map[string]goanalysis.Package, len(model.Packages)),
		directory:         make(map[string][]string, len(model.Packages)),
		testdata:          make(map[string][]string),
		corpus:            make(map[string][]string),
	}
	if len(readers) != 0 {
		sources.extraFiles = make([]string, 0, len(inputs.Files)+len(inputs.Corpus))
		for name := range inputs.Files {
			sources.extraFiles = append(sources.extraFiles, name)
		}
		for name := range inputs.Corpus {
			sources.extraFiles = append(sources.extraFiles, name)
		}
		slices.Sort(sources.extraFiles)
	}
	for _, pkg := range model.Packages {
		sources.packages[pkg.ImportPath] = pkg
	}
	for name := range inputs.Files {
		if owner, ok := testdataOwner(name); ok {
			sources.testdata[owner] = append(sources.testdata[owner], name)
			continue
		}
		directory := path.Dir(name)
		sources.directory[directory] = append(sources.directory[directory], name)
	}
	for name := range inputs.Corpus {
		if owner, target, ok := corpusOwner(name); ok {
			sources.corpus[owner+"\x00"+target] = append(sources.corpus[owner+"\x00"+target], name)
		}
	}
	return sources
}

func testdataOwner(name string) (string, bool) {
	if remainder, found := strings.CutPrefix(name, "testdata/"); found && remainder != "" {
		return ".", true
	}
	if index := strings.Index(name, "/testdata/"); index > 0 {
		return name[:index], true
	}
	return "", false
}

func corpusOwner(name string) (string, string, bool) {
	owner, found := testdataOwner(name)
	if !found {
		return "", "", false
	}
	remainder := name
	if owner != "." {
		remainder = strings.TrimPrefix(name, owner+"/")
	}
	remainder, found = strings.CutPrefix(remainder, "testdata/fuzz/")
	if !found {
		return "", "", false
	}
	target, _, found := strings.Cut(remainder, "/")
	if !found || target == "" {
		return "", "", false
	}
	return owner, target, true
}

func (sources targetKeySources) narrowInputsFor(target goanalysis.Target) evidence.TargetInputs {
	files := make(map[string]string)
	include := func(name string) {
		if digest, known := sources.inputs.Files[name]; known {
			files[name] = digest
			return
		}
		if digest, known := sources.inputs.Corpus[name]; known {
			files[name] = digest
		}
	}
	for _, name := range moduleManifestFiles {
		include(name)
	}
	for _, pkg := range sources.closure(target) {
		for _, name := range sources.directory[pkg.RelativeDir] {
			if pkg.ImportPath != target.Package && strings.HasSuffix(name, "_test.go") {
				continue
			}
			include(name)
		}
		for _, name := range sources.testdata[pkg.RelativeDir] {
			include(name)
		}
		for _, name := range pkg.EmbedFiles {
			include(name)
		}
	}
	inputs := evidence.TargetInputs{
		Files: files, Dependencies: sources.inputs.Dependencies,
		Toolchain: sources.inputs.Toolchain, Platform: sources.inputs.Platform,
		Environment: targetBehaviorEnvironment(sources.inputs.Environment, nil), Contract: sources.contract,
		TestArgs: sources.testArgs, BuildTags: sources.buildTags,
		CommandTimeout: sources.commandTimeout, TargetTimeout: sources.targetTimeout,
		GoatestVersion: sources.inputs.GoatestVersion, GoatestBuild: sources.inputs.GoatestBuild,
		GoMutantsVersion: sources.inputs.GoMutantsVersion,
	}
	if target.Kind == goanalysis.KindFuzz {
		inputs.Corpus = make(map[string]string)
		for _, name := range sources.corpus[target.RelativeDir+"\x00"+target.Name] {
			inputs.Corpus[name] = sources.inputs.Corpus[name]
		}
	}
	return inputs
}

func (sources targetKeySources) wholeTreeInputsFor(target goanalysis.Target) evidence.TargetInputs {
	inputs := sources.narrowInputsFor(target)
	for _, name := range sources.extraFiles {
		if digest, known := sources.inputs.Files[name]; known {
			inputs.Files[name] = digest
			continue
		}
		if digest, known := sources.inputs.Corpus[name]; known {
			inputs.Files[name] = digest
		}
	}
	return inputs
}

func (sources targetKeySources) targetKey(target goanalysis.Target, environment []string, wholeTree bool) string {
	inputs := sources.narrowInputsFor(target)
	inputs.Environment = targetBehaviorEnvironment(sources.inputs.Environment, environment)
	if wholeTree {
		if !sources.repositoryReaders[target.Package] {
			return ""
		}
		inputs = sources.wholeTreeInputsFor(target)
		inputs.Environment = targetBehaviorEnvironment(sources.inputs.Environment, environment)
	}
	return evidence.TargetBehaviorKey(inputs)
}

func targetBehaviorEnvironment(base, overlay []string) []string {
	values := make(map[string]string, len(base)+len(overlay))
	for _, entry := range append(slices.Clone(base), overlay...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		canonical := key
		if runtime.GOOS == "windows" {
			canonical = strings.ToUpper(key)
		}
		values[canonical] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	slices.Sort(result)
	return result
}

func (sources targetKeySources) suiteKey(pkg string, targets []evidence.TargetKey, environment []string, wholeTree bool) string {
	owner, known := sources.packages[pkg]
	if !known {
		return ""
	}
	target := goanalysis.Target{
		Package: pkg, RelativeDir: owner.RelativeDir, Dependencies: owner.Dependencies,
	}
	if wholeTree {
		if !sources.repositoryReaders[pkg] {
			return ""
		}
		inputs := sources.wholeTreeInputsFor(target)
		inputs.Environment = targetBehaviorEnvironment(sources.inputs.Environment, environment)
		return evidence.SuiteBehaviorKey(inputs, targets)
	}
	inputs := sources.narrowInputsFor(target)
	inputs.Environment = targetBehaviorEnvironment(sources.inputs.Environment, environment)
	return evidence.SuiteBehaviorKey(inputs, targets)
}

func (sources targetKeySources) closure(target goanalysis.Target) []goanalysis.Package {
	closure := make([]goanalysis.Package, 0, len(target.Dependencies)+1)
	if own, known := sources.packages[target.Package]; known {
		closure = append(closure, own)
	}
	for _, dependency := range target.Dependencies {
		if dependency == target.Package {
			continue
		}
		if pkg, known := sources.packages[dependency]; known {
			closure = append(closure, pkg)
		}
	}
	return closure
}

type MutationEvidence struct {
	provenance       string
	sources          targetKeySources
	targets          []TargetEvidence
	targetByID       map[targetIdentity]TargetEvidence
	suiteEnvironment []string

	records map[string]evidence.MutationRecord

	keys map[targetIdentity]string

	wholeKeys         map[targetIdentity]string
	baselineWholeTree map[targetIdentity]bool

	passed map[targetIdentity]bool

	suites             map[string]string
	wholeSuites        map[string]string
	suiteBaselineWhole map[string]bool

	mutex    sync.Mutex
	recorded map[string]evidence.MutationRecord
	reused   map[string]string
}

func newMutationEvidence(store evidence.MutationStore, keys map[targetIdentity]string, passed map[targetIdentity]bool, suites map[string]string, provenance string) *MutationEvidence {
	records := make(map[string]evidence.MutationRecord, len(store.Records))
	for _, record := range store.Records {
		records[record.MutantID] = record
	}
	return &MutationEvidence{
		provenance: provenance, records: records, keys: keys, passed: passed, suites: suites,
		targetByID: make(map[targetIdentity]TargetEvidence),
		wholeKeys:  make(map[targetIdentity]string), baselineWholeTree: make(map[targetIdentity]bool),
		wholeSuites: make(map[string]string), suiteBaselineWhole: make(map[string]bool),
		recorded: make(map[string]evidence.MutationRecord),
		reused:   make(map[string]string),
	}
}

func newRunMutationEvidence(store evidence.MutationStore, sources targetKeySources, targets []TargetEvidence, inventory []report.TargetDisposition, suiteEnvironment []string, snapshot string) *MutationEvidence {
	keys := make(map[targetIdentity]string, len(targets))
	baselineWholeTree := make(map[targetIdentity]bool, len(targets))
	for _, target := range targets {
		identity := identify(target.Target)
		keys[identity] = sources.targetKey(target.Target, target.Environment, false)
		baselineWholeTree[identity] = target.WholeTree
	}
	passed := make(map[targetIdentity]bool, len(inventory))
	for _, item := range inventory {
		if item.Status == "passed" {
			passed[targetIdentity{pkg: item.Package, name: item.Name, kind: item.Kind}] = true
		}
	}
	collected := newMutationEvidence(store, keys, passed, suiteKeys(sources, targets, keys, passed, suiteEnvironment, false), "snapshot="+snapshot)
	collected.sources = sources
	collected.targets = slices.Clone(targets)
	collected.suiteEnvironment = slices.Clone(suiteEnvironment)
	collected.targetByID = make(map[targetIdentity]TargetEvidence, len(targets))
	for _, target := range targets {
		collected.targetByID[identify(target.Target)] = target
	}
	collected.baselineWholeTree = baselineWholeTree
	for identity, wholeTree := range baselineWholeTree {
		collected.suiteBaselineWhole[identity.pkg] = collected.suiteBaselineWhole[identity.pkg] || wholeTree
	}
	return collected
}

func suiteKeys(sources targetKeySources, targets []TargetEvidence, keys map[targetIdentity]string, passed map[targetIdentity]bool, environment []string, wholeTree bool) map[string]string {
	byPackage := make(map[string][]evidence.TargetKey, len(sources.packages))
	unmeasured := make(map[string]bool, len(sources.packages))
	for _, target := range targets {
		identity := identify(target.Target)
		key := keys[identity]
		if target.Covered == nil || !passed[identity] || key == "" {
			unmeasured[identity.pkg] = true
			continue
		}
		byPackage[identity.pkg] = append(byPackage[identity.pkg], evidence.TargetKey{
			Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key, WholeTree: wholeTree,
		})
	}
	suites := make(map[string]string, len(sources.packages))
	for path := range sources.packages {
		if unmeasured[path] {
			continue
		}
		if key := sources.suiteKey(path, byPackage[path], environment, wholeTree); key != "" {
			suites[path] = key
		}
	}
	return suites
}

func (collected *MutationEvidence) targetMatches(identity targetIdentity, recorded evidence.TargetKey) bool {
	if recorded.WholeTree {
		key := collected.wholeTargetKey(identity)
		return key != "" && key == recorded.Key
	}
	if collected.baselineWholeTree[identity] {
		return false
	}
	key := collected.keys[identity]
	return key != "" && key == recorded.Key
}

func (collected *MutationEvidence) targetKey(target TargetEvidence) (string, bool) {
	identity := identify(target.Target)
	wholeTree := target.WholeTree || collected.baselineWholeTree[identity]
	if wholeTree {
		return collected.wholeTargetKey(identity), true
	}
	return collected.keys[identity], false
}

func (collected *MutationEvidence) suiteMatches(pkg string, recorded evidence.SuiteKey) bool {
	if recorded.WholeTree {
		key := collected.wholeSuiteKey(pkg)
		return key != "" && key == recorded.Key
	}
	if collected.suiteBaselineWhole[pkg] {
		return false
	}
	key := collected.suites[pkg]
	return key != "" && key == recorded.Key
}

func (collected *MutationEvidence) suiteKey(pkg string, wholeTree bool) (string, bool) {
	wholeTree = wholeTree || collected.suiteBaselineWhole[pkg]
	if wholeTree {
		return collected.wholeSuiteKey(pkg), true
	}
	return collected.suites[pkg], false
}

func (collected *MutationEvidence) wholeTargetKey(identity targetIdentity) string {
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	return collected.wholeTargetKeyLocked(identity)
}

func (collected *MutationEvidence) wholeTargetKeyLocked(identity targetIdentity) string {
	if key, generated := collected.wholeKeys[identity]; generated {
		return key
	}
	target, known := collected.targetByID[identity]
	if !known {
		collected.wholeKeys[identity] = ""
		return ""
	}
	key := collected.sources.targetKey(target.Target, target.Environment, true)
	collected.wholeKeys[identity] = key
	return key
}

func (collected *MutationEvidence) wholeSuiteKey(pkg string) string {
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	if key, generated := collected.wholeSuites[pkg]; generated {
		return key
	}
	keys := make([]evidence.TargetKey, 0)
	for _, target := range collected.targets {
		identity := identify(target.Target)
		if identity.pkg != pkg {
			continue
		}
		if target.Covered == nil || !collected.passed[identity] {
			collected.wholeSuites[pkg] = ""
			return ""
		}
		key := collected.wholeTargetKeyLocked(identity)
		if key == "" {
			collected.wholeSuites[pkg] = ""
			return ""
		}
		keys = append(keys, evidence.TargetKey{
			Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key, WholeTree: true,
		})
	}
	key := collected.sources.suiteKey(pkg, keys, collected.suiteEnvironment, true)
	collected.wholeSuites[pkg] = key
	return key
}

func (collected *MutationEvidence) reuseKill(mutant gomutants.Mutant, route mutationRoute) (string, string, bool) {
	if collected == nil {
		return "", "", false
	}
	record, known := collected.records[mutant.ID]
	if !known || record.Outcome != evidence.MutationOutcomeKilled || len(record.KilledBy) == 0 {
		return "", "", false
	}
	for _, group := range mutationTargetGroups(route.reaching) {
		if !collected.containsTargetSet(group, record.KilledBy) {
			continue
		}
		collected.mutex.Lock()
		collected.reused[mutant.ID] = record.Provenance
		collected.mutex.Unlock()
		return mutationKillRecordDetail(record.KilledBy), record.Provenance, true
	}
	return "", "", false
}

func mutationKillRecordDetail(targets []evidence.TargetKey) string {
	if len(targets) == 1 {
		return targets[0].Name
	}
	return fmt.Sprintf("%s (%d related targets)", targets[0].Package, len(targets))
}

func (collected *MutationEvidence) containsTargetSet(targets []TargetEvidence, recorded []evidence.TargetKey) bool {
	if len(recorded) > len(targets) {
		return false
	}
	for _, candidate := range recorded {
		index := slices.IndexFunc(targets, func(target TargetEvidence) bool {
			identity := identify(target.Target)
			return candidate.Package == identity.pkg && candidate.Name == identity.name && candidate.Kind == identity.kind
		})
		if index < 0 {
			return false
		}
		target := targets[index]
		identity := identify(target.Target)
		if !collected.passed[identity] || !collected.targetMatches(identity, candidate) {
			return false
		}
	}
	return true
}

func (collected *MutationEvidence) reuseVerdict(mutant gomutants.Mutant, route mutationRoute) (evidence.FindingSeed, string, bool) {
	if collected == nil {
		return evidence.FindingSeed{}, "", false
	}
	record, known := collected.records[mutant.ID]
	if !known || record.Finding == nil {
		return evidence.FindingSeed{}, "", false
	}
	switch record.Outcome {
	case evidence.MutationOutcomeSurvived:
		if !collected.exhausts(record.Exhausted, route.reaching) {
			return evidence.FindingSeed{}, "", false
		}
	case evidence.MutationOutcomeUnreached:
		if !collected.suiteAnswers(mutant, record.Suite, route) {
			return evidence.FindingSeed{}, "", false
		}
	default:
		return evidence.FindingSeed{}, "", false
	}
	collected.mutex.Lock()
	collected.reused[mutant.ID] = record.Provenance
	collected.mutex.Unlock()
	return *record.Finding, record.Provenance, true
}

func (collected *MutationEvidence) exhausts(exhausted []evidence.TargetKey, reaching []TargetEvidence) bool {
	if len(reaching) == 0 {
		return false
	}
	for _, target := range reaching {
		identity := identify(target.Target)
		if target.Covered == nil || !collected.passed[identity] {
			return false
		}
		if !slices.ContainsFunc(exhausted, func(candidate evidence.TargetKey) bool {
			return candidate.Package == identity.pkg && candidate.Name == identity.name &&
				candidate.Kind == identity.kind && collected.targetMatches(identity, candidate)
		}) {
			return false
		}
	}
	return true
}

func (collected *MutationEvidence) suiteAnswers(mutant gomutants.Mutant, suite *evidence.SuiteKey, route mutationRoute) bool {
	if suite == nil || len(route.reaching) != 0 || len(route.discharged) != 0 {
		return false
	}
	return suite.Package == mutant.Package && collected.suiteMatches(mutant.Package, *suite)
}

func (collected *MutationEvidence) recordUnreached(mutant gomutants.Mutant, wholeTree bool, kind, summary string) {
	collected.recordSuite(mutant, wholeTree, kind, summary)
}

func (collected *MutationEvidence) recordSuite(mutant gomutants.Mutant, wholeTree bool, kind, summary string) {
	if collected == nil || kind == "" || summary == "" {
		return
	}
	key, wholeTree := collected.suiteKey(mutant.Package, wholeTree)
	if key == "" {
		return
	}
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: filepath.ToSlash(mutant.Path), Package: mutant.Package,
		Outcome: evidence.MutationOutcomeUnreached, Provenance: collected.provenance,
		Suite:   &evidence.SuiteKey{Package: mutant.Package, Key: key, WholeTree: wholeTree},
		Finding: &evidence.FindingSeed{Kind: kind, Summary: summary},
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	collected.recorded[mutant.ID] = record
}

func (collected *MutationEvidence) recordSurvived(mutant gomutants.Mutant, targets []TargetEvidence, kind, summary string) {
	collected.recordExhausted(mutant, targets, kind, summary)
}

func (collected *MutationEvidence) recordExhausted(mutant gomutants.Mutant, targets []TargetEvidence, kind, summary string) {
	if collected == nil || len(targets) == 0 || kind == "" || summary == "" {
		return
	}
	exhausted := make([]evidence.TargetKey, 0, len(targets))
	for _, target := range targets {
		identity := identify(target.Target)
		if target.Covered == nil || !collected.passed[identity] {
			return
		}
		key, wholeTree := collected.targetKey(target)
		if key == "" {
			return
		}
		exhausted = append(exhausted, evidence.TargetKey{
			Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key, WholeTree: wholeTree,
		})
	}
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: filepath.ToSlash(mutant.Path), Package: mutant.Package,
		Outcome: evidence.MutationOutcomeSurvived, Provenance: collected.provenance, Exhausted: exhausted,
		Finding: &evidence.FindingSeed{Kind: kind, Summary: summary},
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	collected.recorded[mutant.ID] = record
}

func (collected *MutationEvidence) recordKill(mutant gomutants.Mutant, targets []TargetEvidence) {
	if collected == nil || len(targets) == 0 {
		return
	}
	killers := make([]evidence.TargetKey, 0, len(targets))
	for _, target := range targets {
		killer := identify(target.Target)
		if killer == (targetIdentity{}) || !collected.passed[killer] {
			return
		}
		key, wholeTree := collected.targetKey(target)
		if key == "" {
			return
		}
		killers = append(killers, evidence.TargetKey{
			Package: killer.pkg, Name: killer.name, Kind: killer.kind, Key: key, WholeTree: wholeTree,
		})
	}
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: filepath.ToSlash(mutant.Path), Package: mutant.Package,
		Outcome: evidence.MutationOutcomeKilled, Provenance: collected.provenance,
		KilledBy: killers,
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	collected.recorded[mutant.ID] = record
}

func (collected *MutationEvidence) disposition(mutantID string) (bool, string) {
	if collected == nil {
		return false, ""
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	provenance, found := collected.reused[mutantID]
	return found, provenance
}

func (collected *MutationEvidence) store(catalog gomutants.Catalog, modulePath string) evidence.MutationStore {
	if collected == nil {
		return evidence.MutationStore{}
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	known := make(map[string]struct{}, len(catalog.Mutants))
	for _, mutant := range catalog.Mutants {
		known[mutant.ID] = struct{}{}
	}
	records := make([]evidence.MutationRecord, 0, len(collected.recorded)+len(collected.records))
	for id, record := range collected.recorded {
		if _, selected := known[id]; selected {
			records = append(records, record)
		}
	}
	for id, record := range collected.records {
		if _, selected := known[id]; !selected {
			continue
		}
		if _, replaced := collected.recorded[id]; replaced {
			continue
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(first, second evidence.MutationRecord) int {
		return strings.Compare(first.MutantID, second.MutantID)
	})
	return evidence.MutationStore{Schema: evidence.MutationSchemaV1, ModulePath: modulePath, Records: records}
}
