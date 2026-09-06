// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

func TestDigestGoatestExecutableReadsExactBytes(t *testing.T) {
	t.Parallel()
	contents := []byte("exact executable bytes")
	path := filepath.Join(t.TempDir(), "goatest")
	if err := os.WriteFile(path, contents, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	want := hex.EncodeToString(sum[:])
	got, err := digestGoatestExecutable(path)
	if err != nil || got != want {
		t.Fatalf("digestGoatestExecutable = (%q, %v), want %q", got, err, want)
	}
}

func TestDigestGoatestExecutableFailsClosed(t *testing.T) {
	t.Parallel()
	if digest, err := digestGoatestExecutable(filepath.Join(t.TempDir(), "missing")); err == nil || digest != "" {
		t.Fatalf("digestGoatestExecutable = (%q, %v)", digest, err)
	}
}
