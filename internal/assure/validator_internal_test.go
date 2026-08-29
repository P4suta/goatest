// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCopyRootsRejectsDestinationInsideSource(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(source, "scratch", "candidate")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateCopyRoots(source, destination); err == nil {
		t.Fatal("candidate clone destination inside repository was accepted")
	}
}

func TestValidateCopyRootsAcceptsSeparateTrees(t *testing.T) {
	if err := validateCopyRoots(t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
