// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

// eventFieldCount is how many fields a terse event row holds once it names a
// test: the action, the package and the test.
//
// A row with one fewer is a package-level event, which is a shape this tool has
// to tell apart from a test's.
const eventFieldCount = 3

// minimumEventFields is the shortest row events accepts: an action and a
// package.
const minimumEventFields = 2

// events builds a `go test -json` stream from terse "action package test"
// rows, so a test reads as the run it describes.
func events(t *testing.T, rows ...string) string {
	t.Helper()
	var builder strings.Builder
	for _, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < minimumEventFields {
			t.Fatalf("event row %q needs at least an action and a package", row)
		}
		builder.WriteString(`{"Action":"` + fields[0] + `","Package":"` + fields[1] + `"`)
		if len(fields) == eventFieldCount {
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
	const wantPackages = 2
	if len(summary.packages) != wantPackages {
		t.Fatalf("packages = %d, want %d", len(summary.packages), wantPackages)
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
	const wantSkipped = 2
	if summary.skipped != wantSkipped {
		t.Fatalf("skipped = %d, want %d: a skipped subtest is a test that did not run",
			summary.skipped, wantSkipped)
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

// TestAuditRefusesARunThatWasNarrowedBeforeItStarted covers the one hole the
// pass/fail/skip accounting cannot see. A test excluded by -test.run was never
// started, so it is not a verdict of any kind and the totals balance over a
// suite that is missing most of itself.
func TestAuditRefusesARunThatWasNarrowedBeforeItStarted(t *testing.T) {
	t.Parallel()
	stream := `{"Action":"output","Package":"example.test/a","Output":"` +
		NarrowedFilterMarker + `: GOATEST_INTERNAL_ASSURE_TEST_RUN=\"^TestOne$\"\n"}` + "\n" +
		events(t, "pass example.test/a TestOne")
	summary, err := audit(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	narrowed := summary.narrowedPackages()
	if len(narrowed) != 1 {
		t.Fatalf("narrowedPackages() = %q, want the one announcement", narrowed)
	}
	if !strings.HasPrefix(narrowed[0], "example.test/a: ") {
		t.Errorf("narrowedPackages() = %q, want it charged to its package", narrowed[0])
	}
	if summary.passed != 1 {
		t.Errorf("passed = %d, want 1: the marker must not disturb the counts", summary.passed)
	}
}

// TestAuditIgnoresOrdinaryPackageOutput keeps the marker check off the ordinary
// lines a package prints, which would otherwise fail every run.
func TestAuditIgnoresOrdinaryPackageOutput(t *testing.T) {
	t.Parallel()
	stream := `{"Action":"output","Package":"example.test/a","Output":"ok  \texample.test/a\t0.01s\n"}` + "\n"
	summary, err := audit(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if narrowed := summary.narrowedPackages(); len(narrowed) != 0 {
		t.Fatalf("narrowedPackages() = %q, want none", narrowed)
	}
}

// TestTheNarrowedFilterMarkerIsSpeltTheSameInBothPlaces is a two-element
// ledger.
//
// The package that prints the marker cannot import this command - it is a
// main package, and a production file here may not import the test harness
// either - so the string exists twice. A marker spelt twice is a marker that
// will eventually be spelt two ways, and the failure mode is silent: the
// printer announces a narrowed run and the reader never recognises it.
func TestTheNarrowedFilterMarkerIsSpeltTheSameInBothPlaces(t *testing.T) {
	t.Parallel()
	const printer = "../../assure/main_test.go"
	source, err := os.ReadFile(printer)
	if err != nil {
		t.Fatalf("read %s: %v", printer, err)
	}
	quoted := strconv.Quote(NarrowedFilterMarker)
	if !strings.Contains(string(source), quoted) {
		t.Fatalf("%s does not declare the marker as %s.\n\n"+
			"The two spellings have drifted, so a narrowed run would announce itself\n"+
			"in words this tool no longer recognises.", printer, quoted)
	}
}
