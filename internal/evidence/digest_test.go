// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/P4suta/goatest/internal/evidence"
)

func TestDigestIsDeterministicAndEveryInputInvalidatesIt(t *testing.T) {
	base := evidence.Inputs{
		Files:            map[string]string{"a.go": "aaa", "a_test.go": "bbb"},
		Dependencies:     map[string]string{"example.com/dep": "v1.2.3:sum"},
		Toolchain:        "go1.26.5",
		Platform:         "windows/amd64",
		Environment:      []string{"B=2", "A=1"},
		Resources:        map[string]string{"postgres": "provider-v1"},
		Corpus:           map[string]string{"FuzzX/seed": "ccc"},
		Contract:         "standard-v1",
		GoatestVersion:   "v0.1.0",
		GoMutantsVersion: "v0.1.0",
	}
	want := evidence.Digest(base)
	reordered := base
	reordered.Environment = []string{"A=1", "B=2"}
	if got := evidence.Digest(reordered); got != want {
		t.Errorf("environment order changed digest: %s != %s", got, want)
	}
	mutations := []func(*evidence.Inputs){
		func(v *evidence.Inputs) { v.Files = map[string]string{"a.go": "changed"} },
		func(v *evidence.Inputs) { v.Dependencies = map[string]string{"example.com/dep": "v2"} },
		func(v *evidence.Inputs) { v.Toolchain = "go1.27" },
		func(v *evidence.Inputs) { v.Platform = "linux/amd64" },
		func(v *evidence.Inputs) { v.Environment = []string{"A=changed", "B=2"} },
		func(v *evidence.Inputs) { v.Resources = map[string]string{"postgres": "provider-v2"} },
		func(v *evidence.Inputs) { v.Corpus = map[string]string{"FuzzX/seed": "changed"} },
		func(v *evidence.Inputs) { v.Contract = "deep-v1" },
		func(v *evidence.Inputs) { v.GoatestVersion = "v0.2.0" },
		func(v *evidence.Inputs) { v.GoMutantsVersion = "v0.2.0" },
	}
	for i, mutate := range mutations {
		candidate := base.Clone()
		mutate(&candidate)
		if got := evidence.Digest(candidate); got == want {
			t.Errorf("mutation %d did not invalidate digest", i)
		}
	}
}

func TestInputsCloneIsEqualAndOwnsEveryMutableCollection(t *testing.T) {
	original := evidence.Inputs{
		Files:        map[string]string{"a.go": "one"},
		Dependencies: map[string]string{"example/dep": "v1"},
		Environment:  []string{"A=1"},
		Resources:    map[string]string{"db": "v1"},
		Corpus:       map[string]string{"FuzzX/seed": "one"},
		Contract:     "standard-v1",
	}
	clone := original.Clone()
	if !reflect.DeepEqual(clone, original) {
		t.Fatalf("clone = %+v, want %+v", clone, original)
	}
	clone.Files["a.go"] = "two"
	clone.Dependencies["example/dep"] = "v2"
	clone.Environment[0] = "A=2"
	clone.Resources["db"] = "v2"
	clone.Corpus["FuzzX/seed"] = "two"
	if original.Files["a.go"] != "one" || original.Dependencies["example/dep"] != "v1" || original.Environment[0] != "A=1" || original.Resources["db"] != "v1" || original.Corpus["FuzzX/seed"] != "one" {
		t.Fatalf("mutating clone changed original: %+v", original)
	}
}

func TestScanHashesContentModeAndCorpusSeparately(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{
		"source.go":                       "package fixture\n",
		"source_test.go":                  "package fixture\n",
		"testdata/fuzz/FuzzX/seed":        "[]byte seed",
		"nested/testdata/fuzz/FuzzY/seed": "nested seed",
		".git/objects/ignored":            "git",
		".goatest/cache/ignored":          "cache",
		"reports/mutation/ignored":        "report",
		"dist/release/ignored":            "binary",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, corpus, err := evidence.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || len(corpus) != 2 || files["source.go"] == "" || corpus["testdata/fuzz/FuzzX/seed"] == "" || corpus["nested/testdata/fuzz/FuzzY/seed"] == "" {
		t.Fatalf("files/corpus = %v / %v", files, corpus)
	}
}
