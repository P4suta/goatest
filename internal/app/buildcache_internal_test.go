// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/report"
)

func TestBuildCacheLocationResolvesTheProgramAndTheLayerTheMachineKeeps(t *testing.T) {
	t.Parallel()
	userCache := t.TempDir()
	perMachine := filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)
	configured := filepath.Join(t.TempDir(), "elsewhere")
	for _, test := range []struct {
		name         string
		executable   string
		userCacheDir func() (string, error)
		contents     string
		wantProgram  string
		wantBase     string
		// wantRepositoryBase is a base below the repository root, which only
		// the running test knows the name of.
		wantRepositoryBase string
	}{
		{
			name:         "the per-machine layer",
			executable:   "/opt/bin/goatest",
			userCacheDir: func() (string, error) { return userCache, nil },
			wantProgram:  "/opt/bin/goatest", wantBase: perMachine,
		},
		{
			name:         "a relative layer the project asked for",
			executable:   "/opt/bin/goatest",
			userCacheDir: func() (string, error) { return userCache, nil },
			contents:     "version = 1\n[cache]\nbuild_dir = \".goatest/build\"\n",
			wantProgram:  "/opt/bin/goatest", wantRepositoryBase: filepath.Join(".goatest", "build"),
		},
		{
			name:         "an absolute layer the project asked for",
			executable:   "/opt/bin/goatest",
			userCacheDir: func() (string, error) { return "", errors.New("no cache directory") },
			contents:     "version = 1\n[cache]\nbuild_dir = " + tomlString(configured) + "\n",
			wantProgram:  "/opt/bin/goatest", wantBase: configured,
		},
		{
			name:       "no user cache directory and no configured one",
			executable: "/opt/bin/goatest",
			// A machine that cannot name a cache root has nowhere to keep a
			// layer, which is a run without a build cache and not a failure.
			userCacheDir: func() (string, error) { return "", errors.New("no cache directory") },
		},
		{
			// A service nobody named an executable for is a process that is not
			// a goatest binary: a test binary running the service in-process,
			// or an application that embedded it. Handing the go command such a
			// path would leave it waiting on a program that never speaks the
			// protocol.
			name:         "a service that was given no executable",
			userCacheDir: func() (string, error) { return userCache, nil },
		},
		{
			name:         "a configuration that will not load",
			executable:   "/opt/bin/goatest",
			userCacheDir: func() (string, error) { return userCache, nil },
			// The run loads the same file a moment later and reports it there,
			// which is the layer that owns the failure.
			contents: "version = 2\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if test.contents != "" {
				if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(test.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wantBase := test.wantBase
			if test.wantRepositoryBase != "" {
				wantBase = filepath.Join(root, test.wantRepositoryBase)
			}
			service := Service{Executable: test.executable, userCacheDir: test.userCacheDir}
			program, base := service.buildCacheLocation(root)
			if program != test.wantProgram || base != wantBase {
				t.Fatalf("buildCacheLocation = (%q, %q), want (%q, %q)", program, base, test.wantProgram, wantBase)
			}
		})
	}
}

func TestCacheStatusAndCollectionReachTheBuildCache(t *testing.T) {
	root := t.TempDir()
	// One byte is a bound the single stored object cannot meet, so the entry is
	// collected for its size rather than its age and the test states nothing
	// about a clock it does not own.
	if err := os.WriteFile(filepath.Join(root, config.FileName),
		[]byte("version = 1\n[cache]\nbuild_max_bytes = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userCache := t.TempDir()
	layer := buildcache.Layer{Dir: filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	stored := time.Now()
	if _, err := layer.Put(buildCacheIdentifier(1), buildCacheIdentifier(2), strings.NewReader("compiled"), 8, stored); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard,
		Executable:   "/opt/bin/goatest",
		userCacheDir: func() (string, error) { return userCache, nil },
		// The collection runs a day after the entry was written, so the window
		// that protects an entry a live build just read has passed.
		Now: func() time.Time { return stored.Add(24 * time.Hour) },
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || status.Verdict != report.VerdictCompleted {
		t.Fatalf("cache status = %+v, %v", status, err)
	}
	if !hasEvidenceDetail(status, "build-status", "entries=1 bytes=8") {
		t.Fatalf("cache status evidence = %+v, want the build cache reported", status.Evidence)
	}
	if !hasEvidenceDetail(status, "policy", "build-max-bytes=1") {
		t.Fatalf("cache policy evidence = %+v, want the build cache bound reported", status.Evidence)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || collected.Verdict != report.VerdictCompleted {
		t.Fatalf("cache gc = %+v, %v", collected, err)
	}
	if !hasEvidenceDetail(collected, "build-gc", "removed-actions=1 removed-objects=1 removed-bytes=8") {
		t.Fatalf("cache gc evidence = %+v, want the over-budget build entry collected", collected.Evidence)
	}
	after, err := layer.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if after.Entries != 0 || after.Bytes != 0 {
		t.Fatalf("build cache after gc = %+v, want it emptied", after)
	}
}

func TestCacheStatusSurvivesAMachineWithNoBuildCacheLocation(t *testing.T) {
	service := Service{
		Root: t.TempDir(), Progress: io.Discard,
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || status.Verdict != report.VerdictCompleted {
		t.Fatalf("cache status = %+v, %v", status, err)
	}
	if !hasEvidenceDetail(status, "build-status", "entries=0 bytes=0") {
		t.Fatalf("cache status evidence = %+v, want an empty build cache reported", status.Evidence)
	}
}

func hasEvidenceDetail(result report.Report, id, detail string) bool {
	for _, item := range result.Evidence {
		if item.ID == id && strings.Contains(item.Detail, detail) {
			return true
		}
	}
	return false
}

// buildCacheIdentifier renders a cache identifier of the length the go command
// uses.
func buildCacheIdentifier(value byte) []byte {
	identifier := make([]byte, 32)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

// tomlString renders one path as a TOML string. The paths a test makes hold
// no quote or backslash on the platforms this runs on, so the rendering is the
// obvious one.
func tomlString(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}
