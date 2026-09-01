// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/config"
)

func TestLoadIsOptionalStrictAndVersioned(t *testing.T) {
	empty, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty.Contract != "standard-v1" || empty.Version != 1 {
		t.Errorf("defaults = %+v", empty)
	}

	root := t.TempDir()
	contents := `version = 1
contract = "deep-v1"

[resources.postgres]
command = ["provider", "postgres"]
timeout = "45s"
shared = true
environment = ["DOCKER_HOST"]

[generation]
command = ["generator"]
allowed_paths = ["internal/**"]
environment = ["GENERATION_API_TOKEN"]

[execution]
environment = ["FEATURE_MODE"]

[[acceptance]]
id = "finding-a"
reason = "equivalent"
expires = "2026-12-31T00:00:00Z"
`
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contract != "deep-v1" || loaded.Resources["postgres"].Timeout != 45*time.Second || !loaded.Resources["postgres"].Shared {
		t.Errorf("loaded = %+v", loaded)
	}
	if !slices.Equal(loaded.Resources["postgres"].Environment, []string{"DOCKER_HOST"}) {
		t.Errorf("resource environment = %v", loaded.Resources["postgres"].Environment)
	}
	if len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" {
		t.Errorf("acceptance = %+v", loaded.Acceptance)
	}
	if !slices.Equal(loaded.Execution.Environment, []string{"FEATURE_MODE"}) || !slices.Equal(loaded.Generation.Environment, []string{"GENERATION_API_TOKEN"}) {
		t.Errorf("environment allowlists = execution %v generation %v", loaded.Execution.Environment, loaded.Generation.Environment)
	}
}

func TestLoadCanonicalizesStandardTestBinaryShorthand(t *testing.T) {
	root := t.TempDir()
	contents := "version = 1\n[execution]\ntest_binary_args = [\"-short\", \"-custom=value\"]\n"
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.Execution.TestBinaryArgs, []string{"-test.short=true", "-custom=value"}; !slices.Equal(got, want) {
		t.Fatalf("test binary args = %v, want %v", got, want)
	}
}

func TestLoadRejectsInvalidAndAmbiguousEnvironmentNames(t *testing.T) {
	for _, test := range []struct {
		name, section, values, want string
	}{
		{name: "execution assignment", section: "execution", values: `"TOKEN=value"`, want: `execution environment name "TOKEN=value" is invalid`},
		{name: "generation punctuation", section: "generation", values: `"TOKEN-NAME"`, want: `generation environment name "TOKEN-NAME" is invalid`},
		{name: "case duplicate", section: "generation", values: `"Token", "TOKEN"`, want: `generation environment name "TOKEN" is duplicated`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			contents := "version = 1\n[" + test.section + "]\nenvironment = [" + test.values + "]\n"
			if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
	root := t.TempDir()
	contents := "version = 1\n[resources.db]\ncommand = [\"provider\"]\nenvironment = [\"TOKEN=value\"]\n"
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), `resource "db" environment name "TOKEN=value" is invalid`) {
		t.Fatalf("resource environment error = %v", err)
	}
}

func TestLoadRejectsUnknownKeysAndInvalidContracts(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown":               "version = 1\ncontrcat = \"standard-v1\"\n",
		"version":               "version = 2\n",
		"contract":              "version = 1\ncontract = \"shallow\"\n",
		"resource-name":         "version = 1\n[resources.\"\"]\ncommand = [\"provider\"]\n",
		"resource-command":      "version = 1\n[resources.db]\ncommand = []\n",
		"resource-first-arg":    "version = 1\n[resources.db]\ncommand = [\"\"]\n",
		"resource-timeout":      "version = 1\n[resources.db]\ncommand = [\"provider\"]\ntimeout = \"later\"\n",
		"resource-zero":         "version = 1\n[resources.db]\ncommand = [\"provider\"]\ntimeout = \"0s\"\n",
		"resource-negative":     "version = 1\n[resources.db]\ncommand = [\"provider\"]\ntimeout = \"-1s\"\n",
		"resource-mode":         "version = 1\n[resources.db]\ncommand = [\"provider\"]\nshared = true\nexclusive = true\n",
		"acceptance-id":         "version = 1\n[[acceptance]]\nreason = \"reviewed\"\nexpires = \"2026-12-31T00:00:00Z\"\n",
		"acceptance-reason":     "version = 1\n[[acceptance]]\nid = \"finding\"\nexpires = \"2026-12-31T00:00:00Z\"\n",
		"acceptance-expires":    "version = 1\n[[acceptance]]\nid = \"finding\"\nreason = \"reviewed\"\n",
		"acceptance-timestamp":  "version = 1\n[[acceptance]]\nid = \"finding\"\nreason = \"reviewed\"\nexpires = \"tomorrow\"\n",
		"acceptance-duplicate":  "version = 1\n[[acceptance]]\nid = \"finding\"\nreason = \"one\"\nexpires = \"2026-12-31T00:00:00Z\"\n[[acceptance]]\nid = \"finding\"\nreason = \"two\"\nexpires = \"2026-12-31T00:00:00Z\"\n",
		"acceptance-whitespace": "version = 1\n[[acceptance]]\nid = \" finding\"\nreason = \"reviewed\"\nexpires = \"2026-12-31T00:00:00Z\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(root); err == nil {
				t.Fatal("Load succeeded, want strict rejection")
			}
		})
	}
}

func TestLoadRejectsUnsafeMalformedAndDuplicateProjectExcludes(t *testing.T) {
	for _, pattern := range []string{"", "../secret", "/absolute", `windows\\path`, "[", "generated/**", "generated/**"} {
		root := t.TempDir()
		patterns := `"` + pattern + `"`
		if pattern == "generated/**" {
			patterns = `"generated/**", "generated/**"`
		}
		contents := "version = 1\n[project]\nexclude = [" + patterns + "]\n"
		if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), "project exclude pattern") {
			t.Fatalf("exclude %q error = %v", pattern, err)
		}
	}
}

func TestLoadReportsReadErrorsAndPreservesDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, config.FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load error = %v, want read config", err)
	}

	loaded, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || loaded.Contract != "standard-v1" || loaded.Resources == nil || len(loaded.Resources) != 0 {
		t.Fatalf("defaults = %+v", loaded)
	}
}

func TestLoadAppliesResourceDefaultsAndOwnsDecodedSlices(t *testing.T) {
	root := t.TempDir()
	contents := "version = 1\n[resources.db]\ncommand = [\"provider\", \"db\"]\n[generation]\ncommand = [\"generate\"]\nallowed_paths = [\"**/*_test.go\"]\n"
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	resource := loaded.Resources["db"]
	if resource.Timeout != 30*time.Second || strings.Join(resource.Command, " ") != "provider db" {
		t.Fatalf("resource = %+v", resource)
	}
	if strings.Join(loaded.Generation.Command, " ") != "generate" || strings.Join(loaded.Generation.AllowedPaths, " ") != "**/*_test.go" {
		t.Fatalf("generation = %+v", loaded.Generation)
	}
}

func TestLoadDistinguishesMalformedAndNonPositiveResourceTimeouts(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		timeout string
		want    string
	}{
		{name: "malformed", timeout: "later", want: "is invalid"},
		{name: "zero", timeout: "0s", want: "is not positive"},
		{name: "negative", timeout: "-1s", want: "is not positive"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			contents := "version = 1\n[resources.db]\ncommand = [\"provider\"]\ntimeout = \"" + testCase.timeout + "\"\n"
			if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Load error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestInitAndAddAcceptanceRoundTripWithoutWeakeningStrictness(t *testing.T) {
	root := t.TempDir()
	if err := config.Init(root); err != nil {
		t.Fatal(err)
	}
	if err := config.Init(root); err == nil {
		t.Fatal("Init overwrote an existing config")
	} else if !strings.Contains(err.Error(), config.FileName+" already exists") {
		t.Fatalf("Init duplicate error = %v", err)
	}
	expires := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if err := config.AddAcceptance(root, config.Acceptance{ID: "finding-a", Reason: "reviewed boundary equivalence", Expires: expires}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "finding-a" || !loaded.Acceptance[0].Expires.Equal(expires) {
		t.Fatalf("acceptance = %+v", loaded.Acceptance)
	}
	if err := config.AddAcceptance(root, config.Acceptance{ID: "finding-a", Reason: "duplicate", Expires: expires}); err == nil {
		t.Fatal("duplicate acceptance was allowed")
	}
}

func TestInitWritesAnnotatedDefaultsAndReportsCreateFailure(t *testing.T) {
	root := t.TempDir()
	if err := config.Init(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	// The template documents every section, but only version and contract are
	// active: loading the file yields exactly the strict defaults.
	var active []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			active = append(active, trimmed)
		}
	}
	if want := []string{"version = 1", `contract = "standard-v1"`}; !slices.Equal(active, want) {
		t.Fatalf("active lines = %q, want %q", active, want)
	}
	for _, section := range []string{
		"[project]", "[execution]", "[cache]", "[resources.postgres]", "[generation]", "[[acceptance]]",
		"packages", "exclude", "build_tags", "test_binary_args", "environment", "timeout", "jobs",
		"max_bytes", "ttl", "command", "shared", "allowed_paths", "id", "reason", "expires",
	} {
		if !strings.Contains(string(data), section) {
			t.Errorf("template omitted %q", section)
		}
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contract != "standard-v1" || !slices.Equal(loaded.Project.Packages, []string{"./..."}) ||
		loaded.Execution.Timeout != 10*time.Minute || loaded.Cache.MaxBytes != 5<<30 || len(loaded.Resources) != 0 {
		t.Fatalf("template drifted from the strict defaults: %+v", loaded)
	}
	missingRoot := filepath.Join(t.TempDir(), "missing", "child")
	if err := config.Init(missingRoot); err == nil || !strings.Contains(err.Error(), "create "+config.FileName) {
		t.Fatalf("Init missing-root error = %v", err)
	}
}

func TestAddAcceptanceRejectsEveryIncompleteFieldAndPropagatesLoadFailure(t *testing.T) {
	expires := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for name, acceptance := range map[string]config.Acceptance{
		"id-empty":     {Reason: "reviewed", Expires: expires},
		"id-space":     {ID: " \t", Reason: "reviewed", Expires: expires},
		"reason-empty": {ID: "finding", Expires: expires},
		"reason-space": {ID: "finding", Reason: " \n", Expires: expires},
		"expiry":       {ID: "finding", Reason: "reviewed"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := config.AddAcceptance(t.TempDir(), acceptance); err == nil || !strings.Contains(err.Error(), "requires id, reason, and expiry") {
				t.Fatalf("AddAcceptance error = %v", err)
			}
		})
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("version = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.AddAcceptance(root, config.Acceptance{ID: "finding", Reason: "reviewed", Expires: expires}); err == nil || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("AddAcceptance load error = %v", err)
	}
}

func TestAddAcceptancePersistsDeterministicIDOrder(t *testing.T) {
	root := t.TempDir()
	if err := config.Init(root); err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"finding-z", "finding-a", "finding-m"} {
		if err := config.AddAcceptance(root, config.Acceptance{ID: id, Reason: "reviewed", Expires: expires}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(loaded.Acceptance))
	for i, acceptance := range loaded.Acceptance {
		got[i] = acceptance.ID
	}
	if strings.Join(got, ",") != "finding-a,finding-m,finding-z" {
		t.Fatalf("acceptance order = %v", got)
	}
}
