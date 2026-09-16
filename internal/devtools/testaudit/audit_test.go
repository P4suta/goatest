// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

// events builds a `go test -json` stream from terse "action package test"
// rows, so a test reads as the run it describes.
func events(t *testing.T, rows ...string) string {
	t.Helper()
	var builder strings.Builder
	for _, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < 2 {
			t.Fatalf("event row %q needs at least an action and a package", row)
		}
		builder.WriteString(`{"Action":"` + fields[0] + `","Package":"` + fields[1] + `"`)
		if len(fields) > 2 {
			builder.WriteString(`,"Test":"` + fields[2] + `"`)
		}
		builder.WriteString("}\n")
	}
	return builder.String()
}

func TestAuditCountsEveryTerminalVerdict(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/a TestOne",
		"pass example.test/a TestOne",
		"run example.test/a TestTwo",
		"fail example.test/a TestTwo",
		"run example.test/b TestThree",
		"skip example.test/b TestThree",
	)))
	if err != nil {
		t.Fatal(err)
	}
	if summary.passed != 1 || summary.failed != 1 || summary.skipped != 1 {
		t.Fatalf("passed=%d failed=%d skipped=%d, want 1/1/1",
			summary.passed, summary.failed, summary.skipped)
	}
	if len(summary.packages) != 2 {
		t.Fatalf("packages = %d, want 2", len(summary.packages))
	}
	if summary.packages[0].pkg != "example.test/a" {
		t.Errorf("packages are not sorted: %q first", summary.packages[0].pkg)
	}
}

// TestAuditSeesAPackageThatOnlySkipped is the whole reason this tool exists: a
// non-verbose `go test` reports that package as `ok`.
func TestAuditSeesAPackageThatOnlySkipped(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/quiet TestOne",
		"skip example.test/quiet TestOne",
		"run example.test/quiet TestTwo",
		"skip example.test/quiet TestTwo",
		"run example.test/loud TestThree",
		"pass example.test/loud TestThree",
	)))
	if err != nil {
		t.Fatal(err)
	}
	silent := summary.silentPackages()
	if !slices.Equal(silent, []string{"example.test/quiet"}) {
		t.Fatalf("silentPackages() = %q, want the quiet package alone", silent)
	}
}

// TestAuditDoesNotCallAPackageWithNoTestFilesSilent keeps the gate off a case
// it would otherwise fail on every day: `go test` emits a package-level skip
// for a package that holds no tests, which is not a suite stepping aside.
func TestAuditDoesNotCallAPackageWithNoTestFilesSilent(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t, "skip example.test/empty")))
	if err != nil {
		t.Fatal(err)
	}
	if silent := summary.silentPackages(); len(silent) != 0 {
		t.Fatalf("silentPackages() = %q, want none", silent)
	}
	if summary.skipped != 0 {
		t.Fatalf("skipped = %d, want 0: a package-level skip is not a skipped test", summary.skipped)
	}
}

func TestAuditReportsOnlyTheSkipsTheLedgerDoesNotAllow(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/a TestRecorded",
		"skip example.test/a TestRecorded",
		"run example.test/a TestUnrecorded",
		"skip example.test/a TestUnrecorded",
		"pass example.test/a TestRan",
	)))
	if err != nil {
		t.Fatal(err)
	}
	unrecorded := summary.unrecordedSkips([]string{"example.test/a TestRecorded"})
	if !slices.Equal(unrecorded, []string{"example.test/a TestUnrecorded"}) {
		t.Fatalf("unrecordedSkips() = %q, want the unrecorded one alone", unrecorded)
	}
}

// TestAuditChargesASkippedSubtestToItsParent pins the ledger's grain. A ledger
// of subtest names would turn over every time somebody adds a case.
func TestAuditChargesASkippedSubtestToItsParent(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"pass example.test/a TestParent",
		"skip example.test/a TestParent/one_case",
		"skip example.test/a TestParent/another_case",
	)))
	if err != nil {
		t.Fatal(err)
	}
	if summary.skipped != 2 {
		t.Fatalf("skipped = %d, want 2: a skipped subtest is a test that did not run", summary.skipped)
	}
	unrecorded := summary.unrecordedSkips([]string{"example.test/a TestParent"})
	if len(unrecorded) != 0 {
		t.Fatalf("unrecordedSkips() = %q, want none once the parent is recorded", unrecorded)
	}
}

// TestAuditRefusesAStreamItCannotRead covers the fail-closed choice: a partial
// answer is indistinguishable from a complete one, so there is no partial
// answer.
func TestAuditRefusesAStreamItCannotRead(t *testing.T) {
	t.Parallel()
	if _, err := audit(strings.NewReader("ok  \texample.test/a\t0.01s\n")); err == nil {
		t.Fatal("a non-JSON line was accepted")
	}
	if _, err := audit(strings.NewReader("{\"Action\":\n")); err == nil {
		t.Fatal("a truncated JSON line was accepted")
	}
}

func TestReadLedgerReadsTheRecordFormat(t *testing.T) {
	t.Parallel()
	path := writeLedger(t, "# a comment\n\nexample.test/a TestOne\nexample.test/b TestTwo\n")
	allowed, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.test/a TestOne", "example.test/b TestTwo"}
	if !slices.Equal(allowed, want) {
		t.Fatalf("readLedger() = %q, want %q", allowed, want)
	}
}

func TestReadLedgerRejectsAMalformedEntry(t *testing.T) {
	t.Parallel()
	path := writeLedger(t, "example.test/a\n")
	if _, err := readLedger(path); err == nil {
		t.Fatal("a line with no test name was accepted")
	}
}

func TestReadLedgerTreatsAMissingRecordAsEmpty(t *testing.T) {
	t.Parallel()
	allowed, err := readLedger(filepath.Join(t.TempDir(), "absent.txt"))
	if err != nil {
		t.Fatalf("a missing ledger was an error: %v", err)
	}
	if len(allowed) != 0 {
		t.Fatalf("readLedger() = %q, want none", allowed)
	}
}

func writeLedger(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ledgerName)
	if err := os.WriteFile(path, []byte(contents), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return path
}
