// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"slices"
	"strconv"
	"time"
)

// TargetInputs is everything the behaviour of one test target can depend on.
// It is an allowlist: a field that is not here is by definition not part of a
// target's behaviour key, and a recorded verdict stays reusable across a change
// to it.
//
// Deliberately excluded, each for a reason a later reader has to be able to
// check:
//
//   - MutationJobs, because parallelism changes how long a run takes and not
//     what it finds.
//   - Packages, PackageScope, the replay selectors, and Changed, because
//     evidence is only recorded under the full-run guard, where all four are at
//     their defaults; a key that hashed them would differ between runs that
//     observed exactly the same thing.
//   - FuzzExecutions, because fuzz evidence is never reused, so no key ever has
//     to distinguish two fuzz budgets.
//   - Trace and KeepTemp, because ADR 0002 keeps diagnostics out of identity:
//     asking for more output about a run must not change what the run proves.
type TargetInputs struct {
	// Files maps a module-relative slash path to its digest: every .go file of
	// the test binary's build closure, every non-.go file under a closure
	// package directory, every //go:embed file, and every declared extra input.
	Files map[string]string
	// Dependencies is metadata.dependencies, the content digest of every
	// external module the target builds against.
	Dependencies map[string]string
	// Toolchain is metadata.toolchain.
	Toolchain string
	// Platform is runtime.GOOS + "/" + runtime.GOARCH.
	Platform string
	// Environment is the selected environment. It is order-independent: the
	// same variables in another order are the same environment.
	Environment []string
	// Contract is the assurance contract the target runs under.
	Contract string
	// TestArgs is order-significant, as configured: the go test command line is
	// read left to right.
	TestArgs []string
	// BuildTags is order-significant, as configured.
	BuildTags []string
	// CommandTimeout bounds one command.
	CommandTimeout time.Duration
	// TargetTimeout bounds one target.
	TargetTimeout time.Duration
	// GoatestVersion is the runner that produced the verdict.
	GoatestVersion string
	// GoMutantsVersion is the mutation engine that produced the mutant.
	GoMutantsVersion string
	// Corpus is the fuzz corpus of a fuzz target, testdata/fuzz/<Name>/**
	// digested; it is empty for every other kind.
	Corpus map[string]string
}

func (inputs TargetInputs) Clone() TargetInputs {
	return TargetInputs{
		Files:            cloneMap(inputs.Files),
		Dependencies:     cloneMap(inputs.Dependencies),
		Toolchain:        inputs.Toolchain,
		Platform:         inputs.Platform,
		Environment:      slices.Clone(inputs.Environment),
		Contract:         inputs.Contract,
		TestArgs:         slices.Clone(inputs.TestArgs),
		BuildTags:        slices.Clone(inputs.BuildTags),
		CommandTimeout:   inputs.CommandTimeout,
		TargetTimeout:    inputs.TargetTimeout,
		GoatestVersion:   inputs.GoatestVersion,
		GoMutantsVersion: inputs.GoMutantsVersion,
		Corpus:           cloneMap(inputs.Corpus),
	}
}

// TargetBehaviorKey identifies the behaviour of one test target, so a later run
// can tell whether a recorded verdict about that target still applies. Two
// runs whose targets have the same key observed the same behaviour; a run that
// sees a different key has to execute the target again.
//
// The key opens with its own domain, so it can never collide with Digest over
// the analogous inputs: the two answer different questions and must not be
// mistaken for each other.
func TargetBehaviorKey(inputs TargetInputs) string {
	h := sha256.New()
	// v2 separates evidence produced with baseline-calibrated command limits
	// from v1 evidence, whose configured command timeout replaced calibration.
	// Development builds can carry the same version string across that runner
	// change, so the key domain itself must mark the execution contract.
	write(h, "goatest-mutation-evidence-key-v2")
	writeMap(h, "files", inputs.Files)
	writeMap(h, "dependencies", inputs.Dependencies)
	write(h, "toolchain", inputs.Toolchain)
	write(h, "platform", inputs.Platform)
	write(h, "environment")
	// Sort a copy: the caller's slice is theirs, and a key that reordered it
	// would change what the caller goes on to record.
	for _, entry := range slices.Sorted(slices.Values(inputs.Environment)) {
		write(h, entry)
	}
	write(h, "contract", inputs.Contract)
	writeList(h, "test-args", inputs.TestArgs)
	writeList(h, "build-tags", inputs.BuildTags)
	write(h, "command-timeout", strconv.FormatInt(int64(inputs.CommandTimeout), 10))
	write(h, "target-timeout", strconv.FormatInt(int64(inputs.TargetTimeout), 10))
	write(h, "goatest", inputs.GoatestVersion)
	write(h, "go-mutants", inputs.GoMutantsVersion)
	writeMap(h, "corpus", inputs.Corpus)
	return hex.EncodeToString(h.Sum(nil))
}

// writeList writes an order-significant list under its field name.
//
// A list that was never configured and a list that was configured empty are
// written differently. They are different configurations, and a key that
// conflated them would let a verdict recorded under one be reused under the
// other.
func writeList(h hash.Hash, domain string, values []string) {
	write(h, domain)
	if values == nil {
		write(h, "absent")
		return
	}
	write(h, "present", strconv.Itoa(len(values)))
	for _, value := range values {
		write(h, value)
	}
}

// SuiteBehaviorKey identifies the behaviour of a package's whole test suite,
// so a later run can tell whether a verdict the suite reached still applies.
//
// A package suite is what settles a mutant no measured target reaches: it runs
// every target of the package, fuzz targets among them as ordinary unit tests,
// as one command. What it observes is therefore the conjunction of what its
// targets observe and of what the package-level run itself reads, and the key
// is built from exactly those two: every target with the behaviour key it had,
// and the package-level run's own inputs. A target whose key moved, one that
// entered, and one that left each change the suite; the order they are counted
// in does not, because a suite is a set.
//
// The key opens with its own domain, so a suite key can never be mistaken for
// the behaviour key of one target: the two answer different questions about
// different things.
func SuiteBehaviorKey(inputs TargetInputs, targets []TargetKey) string {
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, compareTargetKeys)
	h := sha256.New()
	write(h, "goatest-mutation-evidence-suite-key-v2")
	write(h, "targets", strconv.Itoa(len(ordered)))
	for _, target := range ordered {
		write(h, target.Package, target.Name, target.Kind, target.Key)
	}
	write(h, "package", TargetBehaviorKey(inputs))
	return hex.EncodeToString(h.Sum(nil))
}
