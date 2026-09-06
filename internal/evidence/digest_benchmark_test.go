// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"fmt"
	"testing"
)

const (
	digestBenchmarkFileCount       = 10_000
	digestBenchmarkDependencyCount = 500
)

func BenchmarkDigest(b *testing.B) {
	files := make(map[string]string, digestBenchmarkFileCount)
	dependencies := make(map[string]string, digestBenchmarkDependencyCount)
	for index := range digestBenchmarkFileCount {
		files[fmt.Sprintf("internal/package-%04d/file-%04d.go", index/10, index)] = fmt.Sprintf("%064x", index)
	}
	for index := range digestBenchmarkDependencyCount {
		dependencies[fmt.Sprintf("example.test/dependency-%04d", index)] = fmt.Sprintf("%064x", index)
	}
	inputs := Inputs{Files: files, Dependencies: dependencies, Toolchain: "go1.26.6", Platform: "linux/amd64", Contract: "standard-v1", GoatestVersion: "devel", GoMutantsVersion: "v0.1.2"}
	b.ResetTimer()
	for range b.N {
		_ = Digest(inputs)
	}
}
