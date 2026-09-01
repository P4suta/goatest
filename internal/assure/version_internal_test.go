// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestGoMutantsVersionFromFailsClosedAndHonorsReplacements(t *testing.T) {
	// A test binary records no dependency modules at all; the pinned fallback,
	// held to go.mod by its own test, answers for that one shape.
	if version, err := goMutantsVersionFrom(&debug.BuildInfo{}); err != nil || version != goMutantsFallbackVersion {
		t.Fatalf("dependency-free build info = (%q, %v)", version, err)
	}
	// A build that recorded dependencies but linked no go-mutants is a build
	// nothing should attest a version for.
	other := &debug.BuildInfo{Deps: []*debug.Module{{Path: "example.test/other", Version: "v1.0.0"}}}
	if _, err := goMutantsVersionFrom(other); err == nil || !strings.Contains(err.Error(), "absent from build info") {
		t.Fatalf("absent dependency error = %v", err)
	}
	unversioned := &debug.BuildInfo{Deps: []*debug.Module{{Path: goMutantsModulePath}}}
	if _, err := goMutantsVersionFrom(unversioned); err == nil || !strings.Contains(err.Error(), "carries no version") {
		t.Fatalf("unversioned dependency error = %v", err)
	}
	replaced := &debug.BuildInfo{Deps: []*debug.Module{{
		Path: goMutantsModulePath, Version: "v0.1.2",
		Replace: &debug.Module{Path: "example.test/fork", Version: "v0.0.9"},
	}}}
	if version, err := goMutantsVersionFrom(replaced); err != nil || version != "v0.0.9" {
		t.Fatalf("replaced dependency = (%q, %v); the replacement is what actually ran", version, err)
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
