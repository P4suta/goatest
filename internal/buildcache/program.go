// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// usage is what a mistyped invocation prints. The command is not in goatest's
// help: a go command starts it, a person never does.
const usage = `Usage: goatest cacheprog --scratch DIR [--base DIR] [--persist] [--max-bytes N]

Serves the GOCACHEPROG protocol from goatest's own build cache. The go command
starts this; it is not a command to run by hand.`

// Main runs the cache program. It is the whole of the hidden goatest cacheprog
// subcommand: no configuration, no repository, and no environment, because the
// go command that starts it has already been given everything on its command
// line.
func Main(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("goatest cacheprog", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, usage)
		flags.PrintDefaults()
	}
	base := flags.String("base", "", "the persistent layer this machine keeps")
	scratch := flags.String("scratch", "", "the layer this run removes when it ends")
	persist := flags.Bool("persist", false, "write to the base layer instead of the scratch layer")
	maxBytes := flags.Int64("max-bytes", 0, "bound the scratch layer at this many bytes; zero is unbounded")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "goatest: cacheprog takes no arguments, got %q\n", flags.Arg(0))
		flags.Usage()
		return 2
	}
	if *scratch == "" {
		fmt.Fprintln(stderr, "goatest: cacheprog requires --scratch")
		flags.Usage()
		return 2
	}
	if *persist && *base == "" {
		fmt.Fprintln(stderr, "goatest: cacheprog --persist requires --base")
		flags.Usage()
		return 2
	}
	if *maxBytes < 0 {
		fmt.Fprintf(stderr, "goatest: cacheprog --max-bytes %d must not be negative\n", *maxBytes)
		flags.Usage()
		return 2
	}
	layers, err := openLayers(*base, *scratch, *persist, *maxBytes)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	var stats Stats
	if err := Serve(context.Background(), stdin, stdout, layers, &stats); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}
	return 0
}

// openLayers resolves the two layers and creates the directories this process
// writes into. The paths are made absolute here because a response names a file
// the go command opens from its own working directory, which is not this
// process's.
//
// It creates and nothing more. Claiming a directory, probing that it is
// writable, and leaving the marker are the run's job, done once before any go
// command was handed this program: there is one of these processes per go
// command, and a run issues thousands. The base layer is only created when this
// process may write to it, so a build that merely reads never brings a base
// directory into existence.
func openLayers(base, scratch string, persist bool, maxBytes int64) (Layers, error) {
	scratchPath, err := filepath.Abs(scratch)
	if err != nil {
		return Layers{}, fmt.Errorf("goatest: resolve build cache scratch: %w", err)
	}
	layers := Layers{
		Scratch: Layer{Dir: scratchPath, Touch: ScratchTouchInterval},
		Persist: persist, MaxBytes: maxBytes,
	}
	if err := layers.Scratch.ensureWithHooks(layerHooks{}); err != nil {
		return Layers{}, err
	}
	if base == "" {
		return layers, nil
	}
	basePath, err := filepath.Abs(base)
	if err != nil {
		return Layers{}, fmt.Errorf("goatest: resolve build cache base: %w", err)
	}
	layers.Base = Layer{Dir: basePath}
	if persist {
		if err := layers.Base.ensureWithHooks(layerHooks{}); err != nil {
			return Layers{}, err
		}
	}
	return layers, nil
}

// Program renders the GOCACHEPROG value that starts this program.
//
// The go command splits the value with cmd/internal/quoted, so the join here
// has to be that package's: a path with a space in it is a path a developer
// has, and one this rendering would otherwise hand to the go command as two
// arguments.
func Program(program, base, scratch string, persist bool, maxBytes int64) (string, error) {
	arguments := []string{program, "cacheprog", "--base", base, "--scratch", scratch}
	if persist {
		arguments = append(arguments, "--persist")
	}
	if maxBytes > 0 {
		arguments = append(arguments, "--max-bytes", strconv.FormatInt(maxBytes, 10))
	}
	return joinQuoted(arguments)
}

// joinQuoted joins arguments the way cmd/internal/quoted.Split reads them:
// quoted only when necessary, with no escaping inside a quoted argument, which
// is why an argument containing both kinds of quote cannot be rendered at all.
func joinQuoted(arguments []string) (string, error) {
	var rendered strings.Builder
	for index, argument := range arguments {
		if index > 0 {
			rendered.WriteByte(' ')
		}
		var space, single, double bool
		for _, character := range argument {
			switch {
			case character > unicode.MaxASCII:
				continue
			case character == ' ' || character == '\t' || character == '\n' || character == '\r':
				space = true
			case character == '\'':
				single = true
			case character == '"':
				double = true
			}
		}
		switch {
		case !space && !single && !double:
			rendered.WriteString(argument)
		case !single:
			rendered.WriteByte('\'')
			rendered.WriteString(argument)
			rendered.WriteByte('\'')
		case !double:
			rendered.WriteByte('"')
			rendered.WriteString(argument)
			rendered.WriteByte('"')
		default:
			return "", fmt.Errorf("goatest: %q contains both kinds of quote and cannot be passed to the go command", argument)
		}
	}
	return rendered.String(), nil
}

// DefaultBaseName is the base layer's directory below the user cache
// directory. The version is in the name so that a later layout is a new
// directory rather than a migration of somebody's disk.
const DefaultBaseName = "build-v1"

// BaseDirectory resolves where the base layer lives. A configured directory
// wins and is read relative to the repository, so a project that keeps its
// caches on another disk says so once; otherwise the layer is the per-machine
// one the composition root resolved.
//
// It is per machine rather than per repository on purpose: a go command served
// by this cache never reads the user's GOCACHE, so an empty base layer costs a
// full standard library compile. Per repository would pay that for every
// temporary repository a test fixture makes.
func BaseDirectory(root, configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(root, filepath.FromSlash(configured))
}
