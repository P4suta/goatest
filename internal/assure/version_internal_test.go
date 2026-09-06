// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

func TestGoMutantsVersionFromFailsClosedAndHonorsReplacements(t *testing.T) {
	if version, err := goMutantsVersionFrom(&debug.BuildInfo{}); err == nil || version != "" || !strings.Contains(err.Error(), "absent from build info") {
		t.Fatalf("dependency-free build info = (%q, %v)", version, err)
	}
	if version, err := goMutantsVersionFrom(nil); err == nil || version != "" || !strings.Contains(err.Error(), "build info is unavailable") {
		t.Fatalf("missing build info = (%q, %v)", version, err)
	}

	other := &debug.BuildInfo{Deps: []*debug.Module{{Path: "example.test/other", Version: "v1.0.0"}}}
	if _, err := goMutantsVersionFrom(other); err == nil || !strings.Contains(err.Error(), "absent from build info") {
		t.Fatalf("absent dependency error = %v", err)
	}
	unversioned := &debug.BuildInfo{Deps: []*debug.Module{{Path: goMutantsModulePath}}}
	if _, err := goMutantsVersionFrom(unversioned); err == nil || !strings.Contains(err.Error(), "carries no auditable version") {
		t.Fatalf("unversioned dependency error = %v", err)
	}
	development := &debug.BuildInfo{Deps: []*debug.Module{{Path: goMutantsModulePath, Version: "(devel)"}}}
	if _, err := goMutantsVersionFrom(development); err == nil || !strings.Contains(err.Error(), "carries no auditable version") {
		t.Fatalf("development dependency error = %v", err)
	}
	localReplacement := &debug.BuildInfo{Deps: []*debug.Module{{
		Path: goMutantsModulePath, Version: "v0.1.2", Replace: &debug.Module{Path: "../go-mutants"},
	}}}
	if _, err := goMutantsVersionFrom(localReplacement); err == nil || !strings.Contains(err.Error(), "carries no auditable version") {
		t.Fatalf("local replacement error = %v", err)
	}
	replaced := &debug.BuildInfo{Deps: []*debug.Module{{
		Path: goMutantsModulePath, Version: "v0.1.2",
		Replace: &debug.Module{Path: "example.test/fork", Version: "v0.0.9"},
	}}}
	if version, err := goMutantsVersionFrom(replaced); err != nil || version != "v0.0.9" {
		t.Fatalf("replaced dependency = (%q, %v); the replacement is what actually ran", version, err)
	}
	duplicate := &debug.BuildInfo{Deps: []*debug.Module{
		{Path: goMutantsModulePath, Version: "v0.1.2"},
		{Path: goMutantsModulePath, Version: "v0.1.3"},
	}}
	if _, err := goMutantsVersionFrom(duplicate); err == nil || !strings.Contains(err.Error(), "appears more than once") {
		t.Fatalf("duplicate dependency error = %v", err)
	}
}

func TestGoMutantsIdentityUsesAuditableVersionOrExactExecutable(t *testing.T) {
	versioned := &debug.BuildInfo{Deps: []*debug.Module{{Path: goMutantsModulePath, Version: "v1.2.3"}}}
	identity, err := goMutantsIdentityFrom(versioned, func() (string, error) {
		t.Fatal("executable identity read despite auditable module version")
		return "", nil
	})
	if err != nil || identity != "v1.2.3" {
		t.Fatalf("versioned identity = (%q, %v)", identity, err)
	}

	identity, err = goMutantsIdentityFrom(&debug.BuildInfo{}, func() (string, error) {
		return "exact-executable", nil
	})
	if err != nil || identity != "executable-sha256:exact-executable" {
		t.Fatalf("executable identity = (%q, %v)", identity, err)
	}

	cause := errors.New("executable unreadable")
	identity, err = goMutantsIdentityFrom(&debug.BuildInfo{}, func() (string, error) {
		return "", cause
	})
	if identity != "" || !errors.Is(err, cause) || !strings.Contains(err.Error(), "absent from build info") {
		t.Fatalf("unavailable identity = (%q, %v)", identity, err)
	}

	identity, err = goMutantsIdentityFrom(&debug.BuildInfo{}, func() (string, error) {
		return "", nil
	})
	if identity != "" || err == nil || !strings.Contains(err.Error(), "identity is empty") {
		t.Fatalf("empty identity = (%q, %v)", identity, err)
	}
}

func TestResolvedGoatestVersionPrefersStampThenModuleVersion(t *testing.T) {
	stamped := resolvedGoatestVersionFrom("v0.2.0", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}})
	if stamped != "v0.2.0" {
		t.Fatalf("stamped = %q; a release stamp always wins", stamped)
	}
	installed := resolvedGoatestVersionFrom(goatestDevelVersion, &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}})
	if installed != "v0.1.0" {
		t.Fatalf("go-install build = %q; the module version is the truthful identity", installed)
	}
	for name, info := range map[string]*debug.BuildInfo{
		"no build info": nil,
		"devel":         {Main: debug.Module{Version: "(devel)"}},
		"empty":         {},
	} {
		if got := resolvedGoatestVersionFrom(goatestDevelVersion, info); got != goatestDevelVersion {
			t.Fatalf("%s = %q, want the development default", name, got)
		}
	}
}
