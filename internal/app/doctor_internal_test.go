// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
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

// The writability probe proves a directory can be written and leaves the tree
// exactly as it found it: no probe file, and no directory the probe itself
// created.
func TestProbeWritableDirectoryRestoresWhatItCreated(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".goatest")
	if err := probeWritableDirectory(doctorProbeFilesystem{}, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe left the directory it created: %v", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := probeWritableDirectory(doctorProbeFilesystem{}, directory); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe littered the existing directory: %v, %v", entries, err)
	}
}

func TestProbeWritableDirectoryReportsInjectedFailures(t *testing.T) {
	boom := errors.New("disk says no")
	root := t.TempDir()
	directory := filepath.Join(root, "reports")
	if err := probeWritableDirectory(doctorProbeFilesystem{MkdirAll: func(string, os.FileMode) error { return boom }}, directory); !errors.Is(err, boom) {
		t.Fatalf("mkdir failure = %v", err)
	}
	removed := ""
	hooks := doctorProbeFilesystem{
		WriteFile: func(string, []byte, os.FileMode) error { return boom },
		Remove:    func(path string) error { removed = path; return os.Remove(path) },
	}
	if err := probeWritableDirectory(hooks, directory); !errors.Is(err, boom) {
		t.Fatalf("write failure = %v", err)
	}
	if removed != directory {
		t.Fatalf("created directory was not removed after the failed write: %q", removed)
	}
	if err := probeWritableDirectory(doctorProbeFilesystem{Stat: func(string) (os.FileInfo, error) { return nil, boom }}, directory); !errors.Is(err, boom) {
		t.Fatalf("stat failure = %v", err)
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
