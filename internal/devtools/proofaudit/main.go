// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const proofAuditPathArgumentCount = 2

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

const (
	usage = "usage: proofaudit [-module PATH] [-catalog PATH] <trace.jsonl> <profiles-dir>"

	goModFile = "go.mod"

	moduleDirective = "module"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("proofaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, usage)
		flags.PrintDefaults()
	}
	module := flags.String("module", "",
		"the module path the coverage profiles name their files under (default: the module directive of ./go.mod)")
	catalogPath := flags.String("catalog", "",
		"the go-mutants catalog of the recorded tree, carrying the proofs the branch layer decides by "+
			"(default: the branch layer is not audited)")
	if err := flags.Parse(arguments); err != nil {
		return exitUsage
	}
	if flags.NArg() != proofAuditPathArgumentCount {
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

	var catalog *mutantCatalog
	if *catalogPath != "" {
		read, err := readCatalog(*catalogPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "proofaudit: %v\n", err)
			return exitFailure
		}
		catalog = read
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
	result, err := auditTrace(stream, recorded, catalog, auditLayers(catalog))
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
