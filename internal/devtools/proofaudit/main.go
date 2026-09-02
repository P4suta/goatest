// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command proofaudit holds the proof layers of a mutation run to the kills a
// recorded run proved. A layer is any rule that narrows what a mutant is run
// against — today the block routing that keeps a mutant to the coverage blocks
// containing its position, tomorrow the proofs behind it.
//
// The invariant every such layer has to satisfy is that it drops no killer:
// for every mutant a target actually killed, the narrowed rule must still
// route that mutant to that target. A layer that would drop a real killer
// would lose a kill the run proved, which is not an optimisation but a hole in
// the assurance. The report prints that as the violations count, and a sound
// layer prints zero.
//
// It is a developer tool rather than part of the shipped binary, and it reads
// what a run left behind and nothing else:
//
//	go run ./internal/devtools/proofaudit [-module PATH] trace.jsonl <profiles-dir>
//
// The two halves come from one `goatest verify --trace --keep-temp` run: the
// goatest-trace-v1 recording, whose route events say how each mutant was
// routed and whose mutant-exec events say what became of it, and the temporary
// directory of that run, which holds one Go coverage profile per measured
// target. Every number printed comes from a recorded value, so auditing one
// recording twice prints the same bytes on any machine at any moment.
//
// The rules are reimplemented here from the recording rather than called out
// of internal/assure. An audit that asked the code under audit whether it was
// right would prove only that the code agrees with itself; two independent
// implementations disagreeing is the signal this tool exists to raise.
//
// The exit code is a gate: zero when every layer kept every killer, one when a
// layer would drop one or when an input could not be read. The report reaches
// stdout either way, because a violation is something to read rather than
// something to hide behind a failure.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Exit codes. Usage is separated from failure so that a wrapper can tell a
// mistyped command line from a recording it could not audit.
const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

const (
	// usage is the one command line the tool accepts.
	usage = "usage: proofaudit [-module PATH] <trace.jsonl> <profiles-dir>"
	// goModFile is where the module path comes from when the flag does not
	// give one: the module of the repository the tool is run from, which is
	// the module the profiles of its own run name their files under.
	goModFile = "go.mod"
	// moduleDirective opens the line of a go.mod that names the module.
	moduleDirective = "module"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run audits the named recording onto stdout, reporting anything that stopped
// it on stderr. Nothing reaches stdout unless the whole audit was made, so a
// caller that redirects the report to a file never keeps a partial one.
func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("proofaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, usage)
		flags.PrintDefaults()
	}
	module := flags.String("module", "",
		"the module path the coverage profiles name their files under (default: the module directive of ./go.mod)")
	if err := flags.Parse(arguments); err != nil {
		return exitUsage
	}
	if flags.NArg() != 2 {
		flags.Usage()
		return exitUsage
	}
	tracePath, profilesPath := flags.Arg(0), flags.Arg(1)
	modulePath := *module
	if modulePath == "" {
		read, err := moduleFromGoMod(goModFile)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "proofaudit: %v\n", err)
			return exitFailure
		}
		modulePath = read
	}
	recorded, err := readEvidence(profilesPath, modulePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proofaudit: %v\n", err)
		return exitFailure
	}
	stream, err := os.Open(tracePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proofaudit: %v\n", err)
		return exitFailure
	}
	defer func() { _ = stream.Close() }()
	result, err := auditTrace(stream, recorded, auditLayers())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "proofaudit: %s: %v\n", tracePath, err)
		return exitFailure
	}
	_, _ = io.WriteString(stdout, renderAudit(tracePath, profilesPath, modulePath, result))
	if len(result.violations) > 0 {
		return exitFailure
	}
	return exitSuccess
}

// moduleFromGoMod reads the module path out of a go.mod. The directive is
// parsed here rather than with the module tooling because that is the whole of
// the file this tool needs, and a developer tool is not worth a dependency.
func moduleFromGoMod(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the module path from %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		text := strings.TrimSpace(line)
		if comment := strings.Index(text, "//"); comment >= 0 {
			text = strings.TrimSpace(text[:comment])
		}
		rest, directive := strings.CutPrefix(text, moduleDirective)
		// A line that only opens with the word — "modulepath foo" — is not the
		// directive: the path is separated from it by space.
		if !directive || rest == strings.TrimLeft(rest, " \t") {
			continue
		}
		value := strings.TrimSpace(rest)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s names no module", path)
}
