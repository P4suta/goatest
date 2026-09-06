// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"crypto/sha256"
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
	"github.com/P4suta/goatest/internal/filemode"
	"github.com/P4suta/goatest/internal/report"
)

func TestBuildCacheDirectoryIsResolvedWithoutAnExecutable(t *testing.T) {
	t.Parallel()
	userCache := t.TempDir()
	root := t.TempDir()
	service := Service{UserCacheDir: func() (string, error) { return userCache, nil }}
	want := filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)
	if got := service.buildCacheDirectory(root); got != want {
		t.Fatalf("buildCacheDirectory without an executable = %q, want %q", got, want)
	}
	if program, base := service.buildCacheLocation(root); program != "" || base != want {
		t.Fatalf("buildCacheLocation without an executable = (%q, %q), want no program and the directory", program, base)
	}
	withExecutable := service
	withExecutable.Executable = "/opt/bin/goatest"
	if program, base := withExecutable.buildCacheLocation(root); program != "/opt/bin/goatest" || base != want {
		t.Fatalf("buildCacheLocation = (%q, %q), want the program and the directory", program, base)
	}
}

func TestNativeBuildCacheDirectoryMatchesTheGoEnvironment(t *testing.T) {
	t.Parallel()
	userCache := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "native")
	for _, test := range []struct {
		name        string
		environment []string
		resolver    func() (string, error)
		want        string
	}{
		{
			name: "platform default", environment: []string{"PATH=/usr/bin"},
			resolver: func() (string, error) { return userCache, nil }, want: filepath.Join(userCache, nativeGoCacheDirectoryName),
		},
		{
			name: "explicit absolute directory", environment: []string{"GOCACHE=" + explicit},
			resolver: func() (string, error) { return userCache, nil }, want: explicit,
		},
		{
			name: "last declaration wins", environment: []string{"GOCACHE=" + userCache, "gocache=" + explicit},
			resolver: func() (string, error) { return userCache, nil }, want: explicit,
		},
		{
			name: "disabled", environment: []string{"GOCACHE=off"},
			resolver: func() (string, error) { return userCache, nil },
		},
		{
			name: "invalid relative directory", environment: []string{"GOCACHE=relative"},
			resolver: func() (string, error) { return userCache, nil },
		},
		{name: "unavailable platform default", environment: []string{"PATH=/usr/bin"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := Service{Environment: test.environment, UserCacheDir: test.resolver}
			if got := service.nativeBuildCacheDirectory(); got != test.want {
				t.Fatalf("nativeBuildCacheDirectory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildCacheDirectoryIsEmptyWithoutAUserCacheDirResolver(t *testing.T) {
	t.Parallel()
	t.Run("a machine nobody named", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		service := Service{}
		if got := service.buildCacheDirectory(root); got != "" {
			t.Fatalf("buildCacheDirectory without a user cache resolver = %q, want nowhere", got)
		}
		if got := service.buildCacheLayer(root).Dir; got != "" {
			t.Fatalf("buildCacheLayer without a user cache resolver = %q, want nowhere", got)
		}
	})
	t.Run("a layer the project asked for", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, config.FileName),
			[]byte("version = 1\n[cache]\nbuild_dir = \"own-cache\"\n"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		service := Service{}
		want := filepath.Join(root, "own-cache")
		if got := service.buildCacheDirectory(root); got != want {
			t.Fatalf("buildCacheDirectory without a user cache resolver = %q, want %q", got, want)
		}
		if got := service.buildCacheLayer(root).Dir; got != want {
			t.Fatalf("buildCacheLayer without a user cache resolver = %q, want %q", got, want)
		}
	})
}

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

			userCacheDir: func() (string, error) { return "", errors.New("no cache directory") },
			wantProgram:  "/opt/bin/goatest",
		},
		{

			name:         "a service that was given no executable",
			userCacheDir: func() (string, error) { return userCache, nil },
			wantBase:     perMachine,
		},
		{
			name:         "a configuration that will not load",
			executable:   "/opt/bin/goatest",
			userCacheDir: func() (string, error) { return userCache, nil },

			contents:    "version = 2\n",
			wantProgram: "/opt/bin/goatest",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if test.contents != "" {
				if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(test.contents), filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			}
			wantBase := test.wantBase
			if test.wantRepositoryBase != "" {
				wantBase = filepath.Join(root, test.wantRepositoryBase)
			}
			service := Service{Executable: test.executable, UserCacheDir: test.userCacheDir}
			program, base := service.buildCacheLocation(root)
			if program != test.wantProgram || base != wantBase {
				t.Fatalf("buildCacheLocation = (%q, %q), want (%q, %q)", program, base, test.wantProgram, wantBase)
			}
		})
	}
}

func TestCacheStatusAndCollectionReachTheBuildCache(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, config.FileName),
		[]byte("version = 1\n[cache]\nbuild_max_bytes = 1\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	userCache := t.TempDir()
	layer := buildcache.Layer{Dir: filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	stored := time.Now()
	layers := buildcache.Layers{Base: layer, Persist: true}
	if _, err := layers.Put(buildCacheIdentifier(1), buildCacheIdentifier(2), strings.NewReader("compiled"), 8, stored); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Root: root, Progress: io.Discard,
		Executable:   "/opt/bin/goatest",
		UserCacheDir: func() (string, error) { return userCache, nil },

		TempDirectory: t.TempDir(),

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

func TestCacheMaintenanceReportsAndCollectsAbandonedNativeProjections(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	userCache := t.TempDir()
	parent := filepath.Join(userCache, "goatest")
	if err := os.MkdirAll(parent, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	orphan := abandonedRunScratch(t, parent, buildcache.NativeDirectoryPrefix+"dead", 1024)
	moment := time.Now().Add(time.Hour)
	service := Service{
		Root: root, Progress: io.Discard, TempDirectory: t.TempDir(),
		UserCacheDir: func() (string, error) { return userCache, nil },
		Now:          func() time.Time { return moment },
	}
	status, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "status")
	if err != nil || !hasEvidenceDetail(status, "native-cache-orphans", "abandoned=1") {
		t.Fatalf("cache status = (%+v, %v), want the abandoned native projection reported", status.Evidence, err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("status removed the native projection: %v", err)
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || !hasEvidenceDetail(collected, "native-cache-sweep", "removed=1") {
		t.Fatalf("cache gc = (%+v, %v), want the abandoned native projection collected", collected.Evidence, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("native projection after gc = %v, want it removed", err)
	}
}

func TestCacheGCReportsABuildLayerAnotherProcessIsCollecting(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, config.FileName),
		[]byte("version = 1\n[cache]\nbuild_max_bytes = 1\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	userCache := t.TempDir()
	layer := buildcache.Layer{Dir: filepath.Join(userCache, "goatest", buildcache.DefaultBaseName)}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	stored := time.Now()
	layers := buildcache.Layers{Base: layer, Persist: true}
	if _, err := layers.Put(buildCacheIdentifier(1), buildCacheIdentifier(2), strings.NewReader("compiled"), 8, stored); err != nil {
		t.Fatal(err)
	}

	release, held, err := layer.HoldCollection()
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("HoldCollection did not take the lock of a layer nothing else is collecting")
	}
	defer func() { _ = release() }()
	service := Service{
		Root: root, Progress: io.Discard,
		Executable:    "/opt/bin/goatest",
		UserCacheDir:  func() (string, error) { return userCache, nil },
		TempDirectory: t.TempDir(),
		Now:           func() time.Time { return stored.Add(24 * time.Hour) },
	}
	collected, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err != nil || collected.Verdict != report.VerdictCompleted {
		t.Fatalf("cache gc = %+v, %v", collected, err)
	}
	if !hasEvidenceStatus(collected, "build-gc", "skipped") {
		t.Fatalf("cache gc evidence = %+v, want the build collection reported as skipped", collected.Evidence)
	}
	after, err := layer.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if after.Entries != 1 {
		t.Fatalf("build cache after a skipped gc = %+v, want the entry still there", after)
	}
}

func TestCacheStatusSurvivesAMachineWithNowhereToKeepABuildCache(t *testing.T) {
	service := Service{
		Root: t.TempDir(), Progress: io.Discard, TempDirectory: t.TempDir(),
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

func hasEvidenceStatus(result report.Report, id, status string) bool {
	for _, item := range result.Evidence {
		if item.ID == id && item.Status == status {
			return true
		}
	}
	return false
}

func buildCacheIdentifier(value byte) []byte {
	identifier := make([]byte, sha256.Size)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

func tomlString(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}
