// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/filemode"
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
	if err := os.MkdirAll(filepath.Dir(provider), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider, []byte("fixture"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := doctorProviderCommand(root, "tools/provider.exe"); err != nil {
		t.Fatalf("relative provider rejected: %v", err)
	}
	if err := doctorProviderCommand(root, "tools/"); err == nil {
		t.Fatal("provider directory was accepted as a command")
	}
}

func TestProbeWritableDirectoryRestoresWhatItCreated(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".goatest")
	if err := probeWritableDirectory(doctorProbeFilesystem{}, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe left the directory it created: %v", err)
	}
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
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
	const doctorOutputOverflow = 7
	var buffer limitedDoctorBuffer
	input := strings.Repeat("x", doctorOutputLimit+doctorOutputOverflow)
	if written, err := buffer.Write([]byte(input)); err != nil || written != len(input) {
		t.Fatalf("Write = (%d, %v)", written, err)
	}
	if got := buffer.String(); len(got) <= doctorOutputLimit || !strings.HasSuffix(got, "[goatest: doctor output truncated]") {
		t.Fatalf("bounded output length/suffix = %d / %q", len(got), got[len(got)-min(50, len(got)):])
	}
}

func TestDoctorNameSampleKeepsTheListShortAndSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()
	short := []string{"a", "b"}
	if got := doctorNameSample(short); !slices.Equal(got, short) {
		t.Fatalf("sample of a short list = %v, want it whole", got)
	}
	const beyondTheSample = 2
	long := make([]string, 0, doctorNameSampleSize+beyondTheSample)
	for index := range cap(long) {
		long = append(long, fmt.Sprintf("pkg%02d", index))
	}
	got := doctorNameSample(long)
	if len(got) != doctorNameSampleSize+1 || got[doctorNameSampleSize] != "and 2 more" {
		t.Fatalf("sample of %d names = %v", len(long), got)
	}
}

func TestDoctorBehaviourKeysNamesThePackagesThatWidenTheirKey(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
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
	evidence, err := doctorBehaviourKeys(t.Context(), root, os.Environ(), loaded, "go", []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "widened" || !strings.Contains(evidence.Detail, "fixture.example/keys/opaque") ||
		strings.Contains(evidence.Detail, "fixture.example/keys/quiet") {
		t.Fatalf("behaviour keys = %+v, want the subprocess package named alone", evidence)
	}
}
