//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/testkit"
)

func TestDoctorBehaviourKeysNamesThePackagesThatWidenTheirKey(t *testing.T) {
	t.Parallel()
	testkit.GoBinary(t)
	root := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod":           "module fixture.example/keys\n\ngo 1.26.0\n",
		"quiet/quiet.go":   "package quiet\n\nfunc Quiet() int { return 1 }\n",
		"opaque/opaque.go": "package opaque\n\nimport \"os/exec\"\n\nfunc Run() error { return exec.Command(\"true\").Run() }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	loaded := config.Config{Execution: config.Execution{Timeout: time.Minute}}
	evidence, err := doctorBehaviourKeys(t.Context(), nil, root, os.Environ(), loaded, "go", []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "widened" || !strings.Contains(evidence.Detail, "fixture.example/keys/opaque") ||
		strings.Contains(evidence.Detail, "fixture.example/keys/quiet") {
		t.Fatalf("behaviour keys = %+v, want the subprocess package named alone", evidence)
	}
	if !strings.Contains(evidence.Detail, "os/exec 1") {
		t.Fatalf("behaviour keys = %+v, want the reason that widened it", evidence)
	}
}
