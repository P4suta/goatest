// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorOptionalAndBooleanStatusesAreExplicit(t *testing.T) {
	for _, value := range []string{"", os.DevNull, "off", "OFF"} {
		if got := doctorOptionalStatus(value); got != "not-configured" {
			t.Errorf("doctorOptionalStatus(%q) = %q", value, got)
		}
	}
	if got := doctorOptionalStatus("C:/workspace/go.work"); got != "ready" {
		t.Fatalf("configured workspace status = %q", got)
	}
	if doctorBooleanStatus(true) != "ready" || doctorBooleanStatus(false) != "disabled" {
		t.Fatal("doctor boolean statuses changed")
	}
}

func TestDoctorProviderCommandResolvesRepositoryRelativePathsAndRejectsDirectories(t *testing.T) {
	root := t.TempDir()
	provider := filepath.Join(root, "tools", "provider.exe")
	if err := os.MkdirAll(filepath.Dir(provider), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := doctorProviderCommand(root, "tools/provider.exe"); err != nil {
		t.Fatalf("relative provider rejected: %v", err)
	}
	if err := doctorProviderCommand(root, "tools/"); err == nil {
		t.Fatal("provider directory was accepted as a command")
	}
}

func TestLimitedDoctorBufferBoundsOutputAndMarksTruncation(t *testing.T) {
	var buffer limitedDoctorBuffer
	input := strings.Repeat("x", doctorOutputLimit+7)
	if written, err := buffer.Write([]byte(input)); err != nil || written != len(input) {
		t.Fatalf("Write = (%d, %v)", written, err)
	}
	if got := buffer.String(); len(got) <= doctorOutputLimit || !strings.HasSuffix(got, "[goatest: doctor output truncated]") {
		t.Fatalf("bounded output length/suffix = %d / %q", len(got), got[len(got)-min(50, len(got)):])
	}
}
