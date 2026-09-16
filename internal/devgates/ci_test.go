//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/testkit"
)

// The ledger for the workflow.
//
// docs/ci.md describes this repository's own checks in a table, and a table
// about a workflow rots the way every other table does. Worse, the thing it
// describes is the thing that would have caught the rot, so nothing else can.

const (
	// workflowPath is the workflow that runs the checks, relative to the module
	// root.
	workflowPath = ".github/workflows/ci.yml"

	// ciDocumentation is the page that describes them.
	ciDocumentation = "docs/ci.md"

	// jobTableHeading opens the table of jobs on that page.
	jobTableHeading = "| Job | What it runs |"
)

// workflowJob matches a job name: two spaces, a name, a colon, end of line.
//
// The same shape appears under `on:`, where `push:` is a trigger rather than a
// job, so the scan only reads it after the `jobs:` key. The first version did
// not, and reported the push trigger as a job that checks out a shallow clone -
// advice that would have had somebody add fetch-depth to an event.
var workflowJob = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):$`)

// jobsKey opens the mapping of jobs.
const jobsKey = "jobs:"

// backquotedName matches the first `name` of a documentation row.
var backquotedName = regexp.MustCompile("`([^`]+)`")

func TestEveryWorkflowJobIsDocumented(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	jobs := workflowJobs(t, filepath.Join(root, filepath.FromSlash(workflowPath)))
	documented := documentedJobs(t, filepath.Join(root, filepath.FromSlash(ciDocumentation)))
	for _, job := range jobs {
		if !slices.Contains(documented, job) {
			t.Errorf("the workflow runs job %q and %s does not describe it", job, ciDocumentation)
		}
	}
	for _, job := range documented {
		if !slices.Contains(jobs, job) {
			t.Errorf("%s describes job %q, which the workflow no longer runs", ciDocumentation, job)
		}
	}
}

// TestEveryJobThatReadsHistoryAsksForIt is the shallow-clone gate.
//
// A checkout without fetch-depth has no tags and a truncated history, so a run
// asking what changed against a ref it does not hold is told that nothing did.
// That is the same answer as a clean tree and a completely different fact, and
// a job that reads it reports a green check for work it never did.
//
// The rule is checked here rather than trusted to the workflow file because a
// workflow file is edited by people who are thinking about something else, and
// the next edit that drops the line would be silent in exactly the way this gate
// exists to stop.
func TestEveryJobThatReadsHistoryAsksForIt(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(workflowPath)))
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	var shallow []string
	job := ""
	depth := false
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == jobsKey {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if match := workflowJob.FindStringSubmatch(line); match != nil {
			if job != "" && !depth {
				shallow = append(shallow, job)
			}
			job, depth = match[1], false
			continue
		}
		if strings.Contains(line, "fetch-depth:") {
			depth = true
		}
	}
	if job != "" && !depth {
		shallow = append(shallow, job)
	}
	if len(shallow) > 0 {
		t.Errorf("%d job(s) check out a shallow clone: %s\n\n"+
			"Without fetch-depth a job holds no tags and a truncated history, so a\n"+
			"question about a range it does not have is answered \"nothing changed\".",
			len(shallow), strings.Join(shallow, ", "))
	}
}

// TestTheCheckoutIsNotShallowHere is the other half, and the one that runs
// where it matters.
//
// The gate above reads the workflow; this one reads the repository the tests are
// actually running in. They answer different questions - what the file says, and
// what the runner did - and only the second one notices a checkout action that
// changed its defaults.
func TestTheCheckoutIsNotShallowHere(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	command := exec.CommandContext(t.Context(), testkit.GitBinary(t), "-C", root, "rev-parse", "--is-shallow-repository")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ask git whether the clone is shallow: %v", err)
	}
	if strings.TrimSpace(string(output)) == "true" {
		t.Fatal("this clone is shallow, so a changeset scope here would be told that nothing changed")
	}
}

// workflowJobs reads the job names out of the workflow.
func workflowJobs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	var jobs []string
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == jobsKey {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if match := workflowJob.FindStringSubmatch(line); match != nil {
			jobs = append(jobs, match[1])
		}
	}
	return jobs
}

// documentedJobs reads the first column of the job table.
func documentedJobs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", ciDocumentation, err)
	}
	lines := strings.Split(string(data), "\n")
	heading := slices.Index(lines, jobTableHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds the job table heading %q", ciDocumentation, jobTableHeading)
	}
	var documented []string
	for _, line := range lines[heading+2:] {
		if !strings.HasPrefix(line, "| `") {
			break
		}
		match := backquotedName.FindStringSubmatch(line)
		if match == nil {
			break
		}
		documented = append(documented, match[1])
	}
	return documented
}
