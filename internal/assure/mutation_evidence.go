// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"path"
	"path/filepath"
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

// mutationEvidenceFileName is where a repository keeps what earlier runs
// established about its mutants. It sits beside the cache rather than inside
// it, because cache maintenance walks `v1/` and refuses an entry there that is
// not a confined directory.
const mutationEvidenceFileName = "mutation-evidence-v1.json"

// moduleManifestFiles are read by every build in the module, so a change to
// either of them is a change to every test binary.
var moduleManifestFiles = []string{"go.mod", "go.sum"}

// targetIdentity names a target the way a recorded verdict has to name it:
// by what selects it, never by a target ID.
//
// A target ID carries the line the function is declared on, so adding a test
// above another renames every target below it and would discard evidence
// about tests nothing changed. `-test.run=^Name$` selects by package and name,
// and the kind decides how the target is run at all, so those three are the
// identity and the behaviour key answers everything else.
type targetIdentity struct {
	pkg  string
	name string
	kind string
}

// identify names one discovered target.
func identify(target goanalysis.Target) targetIdentity {
	return targetIdentity{pkg: target.Package, name: target.Name, kind: string(target.Kind)}
}

// mutationEvidenceGuarded reports whether this round may read and write
// mutation evidence.
//
// A record is a claim about the whole project, verified from a tree nothing
// had modified, under the default execution options. Every condition here is
// one of those words: a later round verifies a tree an earlier round repaired,
// configured resources carry runtime state a digest cannot see, a changeset or
// package run narrows the claim, and a replay is a reproduction, which is only
// a reproduction if it actually runs. Outside the guard nothing is read,
// nothing is written, and the mutation phase is handed no evidence at all.
func mutationEvidenceGuarded(round int, loaded config.Config, options Options) bool {
	return round == 0 && len(loaded.Resources) == 0 &&
		!options.Changed && !options.PackageScope && defaultPackagePatterns(options.Packages) &&
		options.ReplayMutantID == "" && options.ReplayFindingID == ""
}

// targetKeySources is everything a run already knows that the behaviour of a
// test target can depend on. It is read only: the builder selects from the
// snapshot digests the run computed for its own identity rather than reading
// the tree a second time, so a key can never describe a file the run did not
// verify.
type targetKeySources struct {
	inputs         evidence.Inputs
	model          goanalysis.Model
	contract       string
	testArgs       []string
	buildTags      []string
	commandTimeout time.Duration
	targetTimeout  time.Duration
	// repositoryReaders are the packages goanalysis found reading a directory
	// they compute rather than a file they name, by import path. A target of
	// one of them is keyed on extraFiles instead of on its closure alone.
	repositoryReaders map[string]bool
	// extraFiles are the module-relative paths a target keys beyond what its
	// closure names: every file of the snapshot, in sorted order, for the
	// targets whose verdict any of them could change. It is empty when the run
	// found no repository reader, because nothing else declares one.
	extraFiles []string

	packages  map[string]goanalysis.Package
	directory map[string][]string
	testdata  map[string][]string
	corpus    map[string][]string
}

// newTargetKeySources indexes the run's own inputs by the questions a key asks
// of them: what lies in a package's directory, what lies under its testdata,
// and what a fuzz target's corpus holds. The indexes are built once, because
// every target of the run asks the same questions of the same scan.
//
// The whole-tree list is built once too, and only when the run found a package
// that reads one: it is every file the snapshot holds, so a target keyed on it
// keeps a verdict only across a tree that did not change at all.
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

// testdataOwner reports the package directory a testdata file belongs to. A
// testdata directory is data the tests of the package above it read, and go
// tooling never treats one as a package, so the whole subtree answers to that
// package.
func testdataOwner(name string) (string, bool) {
	if remainder, found := strings.CutPrefix(name, "testdata/"); found && remainder != "" {
		return ".", true
	}
	if index := strings.Index(name, "/testdata/"); index > 0 {
		return name[:index], true
	}
	return "", false
}

// corpusOwner reports the package directory and the fuzz target a standard
// corpus entry belongs to. A seed corpus is an input of the target that owns
// the directory it sits in and of no other target.
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

// inputsFor is everything one target's behaviour depends on, as the allowlist
// TargetInputs states it.
//
// The files are the test binary's own: the Go files of its build closure, the
// data beside those packages, the files they embed, and the module manifests
// every build reads. A closure package's own test files are not among them,
// because a dependency's tests are never compiled into this binary — but the
// target's package's test files are, and they are the file most likely to
// change.
//
// A target of a package that reads a directory it computes is the exception,
// and it is a conservative one: its closure is not what decides its verdict,
// because the files it will read are not named anywhere, so every file of the
// snapshot is an input of it. Such a target keeps a recorded verdict across an
// identical tree and across nothing else. Nothing is excluded from testing or
// from reuse by name; the key simply stops claiming to know what the test
// reads.
func (sources targetKeySources) inputsFor(target goanalysis.Target) evidence.TargetInputs {
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
	if sources.repositoryReaders[target.Package] {
		for _, name := range sources.extraFiles {
			include(name)
		}
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
		Environment: sources.inputs.Environment, Contract: sources.contract,
		TestArgs: sources.testArgs, BuildTags: sources.buildTags,
		CommandTimeout: sources.commandTimeout, TargetTimeout: sources.targetTimeout,
		GoatestVersion: sources.inputs.GoatestVersion, GoMutantsVersion: sources.inputs.GoMutantsVersion,
	}
	if target.Kind == goanalysis.KindFuzz {
		inputs.Corpus = make(map[string]string)
		for _, name := range sources.corpus[target.RelativeDir+"\x00"+target.Name] {
			inputs.Corpus[name] = sources.inputs.Corpus[name]
		}
	}
	return inputs
}

// closure is the packages of this module whose sources the target's test
// binary links: its own package and every in-module dependency of that
// package's test binary. A dependency outside the module has no directory here
// and is identified by its module digest instead.
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

// MutationEvidence is what earlier runs established about this repository's
// mutants, indexed for the run in progress, and the collector of what this run
// establishes in turn.
//
// It is read from every mutation worker at once and written from every one of
// them, so collection is guarded; the three indexes are built before the phase
// starts and never change, and are read without a lock for that reason.
type MutationEvidence struct {
	// provenance names this run, in the form a record carries it.
	provenance string
	// records is the evidence earlier runs left, by mutant.
	records map[string]evidence.MutationRecord
	// keys is what each target of this run does, by identity.
	keys map[targetIdentity]string
	// passed is the targets this run's baseline ran on the original tree and
	// saw pass. It is the fresh control a reused kill stands on.
	passed map[targetIdentity]bool

	mutex    sync.Mutex
	recorded map[string]evidence.MutationRecord
	reused   map[string]string
}

// newMutationEvidence indexes a loaded store against what this run knows about
// its own targets. A store that could not be read is simply an empty one: the
// run then executes everything and records what it finds, which is the only
// direction that cannot cost assurance.
func newMutationEvidence(store evidence.MutationStore, keys map[targetIdentity]string, passed map[targetIdentity]bool, provenance string) *MutationEvidence {
	records := make(map[string]evidence.MutationRecord, len(store.Records))
	for _, record := range store.Records {
		records[record.MutantID] = record
	}
	return &MutationEvidence{
		provenance: provenance, records: records, keys: keys, passed: passed,
		recorded: make(map[string]evidence.MutationRecord),
		reused:   make(map[string]string),
	}
}

// newRunMutationEvidence builds the index from what the round has: the store,
// the targets the mutation phase will route between, and the inventory the
// baseline produced.
func newRunMutationEvidence(store evidence.MutationStore, sources targetKeySources, targets []TargetEvidence, inventory []report.TargetDisposition, snapshot string) *MutationEvidence {
	keys := make(map[targetIdentity]string, len(targets))
	for _, target := range targets {
		keys[identify(target.Target)] = evidence.TargetBehaviorKey(sources.inputsFor(target.Target))
	}
	passed := make(map[targetIdentity]bool, len(inventory))
	for _, item := range inventory {
		if item.Status == "passed" {
			passed[targetIdentity{pkg: item.Package, name: item.Name, kind: item.Kind}] = true
		}
	}
	return newMutationEvidence(store, keys, passed, "snapshot="+snapshot)
}

// reuseKill reports the target of this run that answers for a mutant an
// earlier run recorded a kill for, when every condition of the reuse holds.
//
// The conditions are what makes the recorded kill a claim about this run. The
// killer must be a target this run's coverage still routes to the mutant,
// because a target that no longer reaches it proves nothing about it. It must
// be the same target: same package, name, and kind, and the same behaviour
// key, so that nothing its binary reads has changed. And this run's own
// baseline must have run it on the original tree and seen it pass, which is
// the fresh half of the confirmation the recording run made — that run watched
// the mutant fail twice against an original that passed, and this run watches
// the original pass again.
//
// A kill fuzzing found is never believed, because finding an input inside one
// budget is not a claim that the next budget finds one.
func (collected *MutationEvidence) reuseKill(mutant gomutants.Mutant, route mutationRoute) (TargetEvidence, bool) {
	if collected == nil {
		return TargetEvidence{}, false
	}
	record, known := collected.records[mutant.ID]
	if !known || record.Outcome != evidence.MutationOutcomeKilled || record.KilledBy == nil {
		return TargetEvidence{}, false
	}
	killer := targetIdentity{pkg: record.KilledBy.Package, name: record.KilledBy.Name, kind: record.KilledBy.Kind}
	if killer.kind == string(goanalysis.KindFuzz) {
		return TargetEvidence{}, false
	}
	index := slices.IndexFunc(route.reaching, func(target TargetEvidence) bool {
		return identify(target.Target) == killer
	})
	if index < 0 {
		return TargetEvidence{}, false
	}
	if key := collected.keys[killer]; key == "" || key != record.KilledBy.Key {
		return TargetEvidence{}, false
	}
	if !collected.passed[killer] {
		return TargetEvidence{}, false
	}
	collected.mutex.Lock()
	collected.reused[mutant.ID] = record.Provenance
	collected.mutex.Unlock()
	return route.reaching[index], true
}

// reuseVerdict reports the finding an earlier run recorded for a mutant it
// watched survive, when every condition under which that verdict is still this
// run's holds.
//
// A survived verdict is a universal proposition: no test that reaches this
// mutant kills it. The recording run established it of the targets it ran, so
// it is a claim about this run exactly when every target this run's coverage
// routes to the mutant is one of those — the same target, under the same
// behaviour key, and seen to pass by this run's own baseline, which is the
// same fresh control a reused kill stands on. A reaching set that shrank is
// still covered, because a target that no longer reaches the mutant cannot
// kill it; one that grew is not covered at all, because the target that
// entered is one nothing was ever run against.
func (collected *MutationEvidence) reuseVerdict(mutant gomutants.Mutant, route mutationRoute) (evidence.FindingSeed, bool) {
	if collected == nil {
		return evidence.FindingSeed{}, false
	}
	record, known := collected.records[mutant.ID]
	if !known || record.Finding == nil || record.Outcome != evidence.MutationOutcomeSurvived {
		return evidence.FindingSeed{}, false
	}
	if !collected.exhausts(record.Exhausted, route.reaching) {
		return evidence.FindingSeed{}, false
	}
	collected.mutex.Lock()
	collected.reused[mutant.ID] = record.Provenance
	collected.mutex.Unlock()
	return *record.Finding, true
}

// exhausts reports whether a recorded set of executed targets covers every
// target this run routes to a mutant.
//
// The conditions on each of them are the conditions of the whole claim, so one
// target failing any of them ends it. A fuzz target is refused for the reason
// a fuzz kill is refused: exploring one budget without finding an input says
// nothing about the next. A target restored from a checkpoint carries no
// coverage blocks, so routing keeps it for the whole file and the reaching set
// it belongs to is wider than the one any run measured; a claim about a
// measured set is not a claim about that. And a reaching set that is empty
// exhausts nothing: a mutant nothing reaches is a statement about a package
// suite instead.
func (collected *MutationEvidence) exhausts(exhausted []evidence.TargetKey, reaching []TargetEvidence) bool {
	if len(reaching) == 0 {
		return false
	}
	for _, target := range reaching {
		identity := identify(target.Target)
		if identity.kind == string(goanalysis.KindFuzz) || target.Covered == nil || !collected.passed[identity] {
			return false
		}
		key := collected.keys[identity]
		if key == "" {
			return false
		}
		if !slices.ContainsFunc(exhausted, func(candidate evidence.TargetKey) bool {
			return candidate.Package == identity.pkg && candidate.Name == identity.name &&
				candidate.Kind == identity.kind && candidate.Key == key
		}) {
			return false
		}
	}
	return true
}

// recordSurvived remembers that every test reaching a mutant was run against it
// and none of them killed it, so a later run can reuse the universal claim.
//
// A survivor a proof discharged the whole reaching set of is not one of these:
// nothing ran, so there is no set of executed targets to name, and the proofs
// re-derive the verdict for free on the next run anyway.
func (collected *MutationEvidence) recordSurvived(mutant gomutants.Mutant, route mutationRoute, kind, summary string) {
	collected.recordExhausted(mutant, evidence.MutationOutcomeSurvived, route.reaching, kind, summary)
}

// recordExhausted writes down a verdict a set of executed targets established,
// with the behaviour key each of them had, and the finding a later run has to
// be able to raise again from it.
//
// Every condition reuse will check is checked here too, so that a record that
// could never be reused is never written: a fuzz target or a target restored
// from a checkpoint among them, a target this run's baseline did not see pass,
// or a target with no behaviour key leaves the store as it found it.
func (collected *MutationEvidence) recordExhausted(mutant gomutants.Mutant, outcome string, targets []TargetEvidence, kind, summary string) {
	if collected == nil || len(targets) == 0 || kind == "" || summary == "" {
		return
	}
	exhausted := make([]evidence.TargetKey, 0, len(targets))
	for _, target := range targets {
		identity := identify(target.Target)
		if identity.kind == string(goanalysis.KindFuzz) || target.Covered == nil || !collected.passed[identity] {
			return
		}
		key := collected.keys[identity]
		if key == "" {
			return
		}
		exhausted = append(exhausted, evidence.TargetKey{
			Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key,
		})
	}
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: filepath.ToSlash(mutant.Path), Package: mutant.Package,
		Outcome: outcome, Provenance: collected.provenance, Exhausted: exhausted,
		Finding: &evidence.FindingSeed{Kind: kind, Summary: summary},
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	collected.recorded[mutant.ID] = record
}

// recordKill remembers that one target confirmed a kill, so a later run can
// reuse it.
//
// Only a kill this run can attribute to one named target is recorded. A batch
// runs several targets under one selector and the engine reports the failure
// without saying which of them a later run would have to check, and a package
// suite names no target at all: both are left unrecorded rather than recorded
// vaguely. A fuzz target is refused for the reason reuseKill refuses one.
func (collected *MutationEvidence) recordKill(mutant gomutants.Mutant, killer targetIdentity) {
	if collected == nil || killer == (targetIdentity{}) || killer.kind == string(goanalysis.KindFuzz) {
		return
	}
	key := collected.keys[killer]
	if key == "" {
		return
	}
	record := evidence.MutationRecord{
		MutantID: mutant.ID, Path: filepath.ToSlash(mutant.Path), Package: mutant.Package,
		Outcome: evidence.MutationOutcomeKilled, Provenance: collected.provenance,
		KilledBy: &evidence.TargetKey{Package: killer.pkg, Name: killer.name, Kind: killer.kind, Key: key},
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	collected.recorded[mutant.ID] = record
}

// disposition reports whether a mutant's verdict was reused, and the run that
// established it. It is what the report says about the mutant, so it answers
// for a nil collector too: a run that reused nothing reused nothing.
func (collected *MutationEvidence) disposition(mutantID string) (bool, string) {
	if collected == nil {
		return false, ""
	}
	collected.mutex.Lock()
	defer collected.mutex.Unlock()
	provenance, found := collected.reused[mutantID]
	return found, provenance
}

// store is what this run leaves for the next one: everything it established,
// beside everything an earlier run established about a mutant this run's
// catalogue still names.
//
// Pruning by the catalogue is what keeps the store from growing without bound:
// a mutant whose file changed has a new identity and its old record can never
// match anything again. A record this run rewrote wins over the one it was
// read from, because it was established against the tree the next run will
// compare itself with.
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
