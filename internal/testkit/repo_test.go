// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
)

const (
	reexecHelperVariable = "GOATEST_TESTKIT_HELPER"
	reexecHelperMarker   = "testkit-reexec-helper-ok"
)

func TestTestkitReexecHelper(t *testing.T) {
	t.Parallel()
	testkit.SkipUnlessHelper(t, reexecHelperVariable)
	fmt.Println(reexecHelperMarker)
}

func TestRepoFileWritesContentsVerbatimAndCreatesParents(t *testing.T) {
	t.Parallel()
	const contents = "first\r\nsecond\nthird"
	repository := testkit.NewRepo(t).Module("fixture.example/files").File("nested/dir/data.txt", contents)

	stored, err := os.ReadFile(repository.Path("nested/dir/data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != contents {
		t.Fatalf("stored contents = %q, want %q", stored, contents)
	}
	module, err := os.ReadFile(repository.Path("go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(module), "module fixture.example/files\n") || !strings.Contains(string(module), "\ngo 1.") {
		t.Fatalf("go.mod = %q", module)
	}
	if information, err := os.Stat(repository.Root()); err != nil || !information.IsDir() {
		t.Fatalf("Root is not a directory: %v", err)
	}
}

func TestHelperArgvReexecutesTheTestBinary(t *testing.T) {
	t.Parallel()
	argv := testkit.HelperArgv("TestTestkitReexecHelper")
	if len(argv) != 2 || argv[0] != os.Args[0] || argv[1] != "-test.run=^TestTestkitReexecHelper$" {
		t.Fatalf("HelperArgv = %q", argv)
	}

	command := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	command.Env = append(os.Environ(), reexecHelperVariable+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("re-execution failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), reexecHelperMarker) {
		t.Fatalf("re-executed helper output = %q, want %q", output, reexecHelperMarker)
	}
	if testkit.HelperEnabled(reexecHelperVariable) {
		t.Error("HelperEnabled reported an inactive helper as active")
	}
}
