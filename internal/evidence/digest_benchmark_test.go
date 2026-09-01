// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"fmt"
	"testing"
)

func BenchmarkDigest(b *testing.B) {
	files := make(map[string]string, 10_000)
	dependencies := make(map[string]string, 500)
	for index := range 10_000 {
		files[fmt.Sprintf("internal/package-%04d/file-%04d.go", index/10, index)] = fmt.Sprintf("%064x", index)
	}
	for index := range 500 {
		dependencies[fmt.Sprintf("example.test/dependency-%04d", index)] = fmt.Sprintf("%064x", index)
	}
	inputs := Inputs{Files: files, Dependencies: dependencies, Toolchain: "go1.26.6", Platform: "linux/amd64", Contract: "standard-v1", GoatestVersion: "devel", GoMutantsVersion: "v0.1.2"}
	b.ResetTimer()
	for range b.N {
		_ = Digest(inputs)
	}
}
