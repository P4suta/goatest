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

type TargetInputs struct {
	Files map[string]string

	Dependencies map[string]string

	Toolchain string

	Platform string

	Environment []string

	Contract string

	TestArgs []string

	BuildTags []string

	CommandTimeout time.Duration

	TargetTimeout time.Duration

	GoatestVersion string

	GoatestBuild string

	GoMutantsVersion string

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
		GoatestBuild:     inputs.GoatestBuild,
		GoMutantsVersion: inputs.GoMutantsVersion,
		Corpus:           cloneMap(inputs.Corpus),
	}
}

func TargetBehaviorKey(inputs TargetInputs) string {
	h := sha256.New()

	write(h, "goatest-mutation-evidence-key-v3")
	writeMap(h, "files", inputs.Files)
	writeMap(h, "dependencies", inputs.Dependencies)
	write(h, "toolchain", inputs.Toolchain)
	write(h, "platform", inputs.Platform)
	write(h, "environment")

	for _, entry := range slices.Sorted(slices.Values(inputs.Environment)) {
		write(h, entry)
	}
	write(h, "contract", inputs.Contract)
	writeList(h, "test-args", inputs.TestArgs)
	writeList(h, "build-tags", inputs.BuildTags)
	write(h, "command-timeout", strconv.FormatInt(int64(inputs.CommandTimeout), 10))
	write(h, "target-timeout", strconv.FormatInt(int64(inputs.TargetTimeout), 10))
	write(h, "goatest", inputs.GoatestVersion)
	write(h, "goatest-build", inputs.GoatestBuild)
	write(h, "go-mutants", inputs.GoMutantsVersion)
	writeMap(h, "corpus", inputs.Corpus)
	return hex.EncodeToString(h.Sum(nil))
}

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

func SuiteBehaviorKey(inputs TargetInputs, targets []TargetKey) string {
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, compareTargetKeys)
	h := sha256.New()
	write(h, "goatest-mutation-evidence-suite-key-v3")
	write(h, "targets", strconv.Itoa(len(ordered)))
	for _, target := range ordered {
		write(h, target.Package, target.Name, target.Kind, target.Key)
	}
	write(h, "package", TargetBehaviorKey(inputs))
	return hex.EncodeToString(h.Sum(nil))
}
