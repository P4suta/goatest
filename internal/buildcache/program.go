// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	CacheProgramUsageExitCode = 2
	decimalRadix              = 10
)

const usage = `Usage: goatest cacheprog --scratch DIR [--base DIR] [--native-source DIR] [--persist] [--max-bytes N]

Serves the GOCACHEPROG protocol from goatest's own build cache. The go command
starts this; it is not a command to run by hand.`

func Main(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("goatest cacheprog", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, usage)
		flags.PrintDefaults()
	}
	base := flags.String("base", "", "the persistent layer this machine keeps")
	scratch := flags.String("scratch", "", "the layer this run removes when it ends")
	nativeSource := flags.String("native-source", "", "a native Go cache used only for verified misses")
	persist := flags.Bool("persist", false, "write to the base layer instead of the scratch layer")
	maxBytes := flags.Int64("max-bytes", 0, "bound the scratch layer at this many bytes; zero is unbounded")
	if err := flags.Parse(arguments); err != nil {
		return CacheProgramUsageExitCode
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "goatest: cacheprog takes no arguments, got %q\n", flags.Arg(0))
		flags.Usage()
		return CacheProgramUsageExitCode
	}
	if *scratch == "" {
		_, _ = fmt.Fprintln(stderr, "goatest: cacheprog requires --scratch")
		flags.Usage()
		return CacheProgramUsageExitCode
	}
	if *persist && *base == "" {
		_, _ = fmt.Fprintln(stderr, "goatest: cacheprog --persist requires --base")
		flags.Usage()
		return CacheProgramUsageExitCode
	}
	if *maxBytes < 0 {
		_, _ = fmt.Fprintf(stderr, "goatest: cacheprog --max-bytes %d must not be negative\n", *maxBytes)
		flags.Usage()
		return CacheProgramUsageExitCode
	}
	layers, err := openLayers(*base, *scratch, *nativeSource, *persist, *maxBytes)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return CacheProgramUsageExitCode
	}
	var stats Stats
	if err := Serve(context.Background(), stdin, stdout, layers, &stats); err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return CacheProgramUsageExitCode
	}
	return 0
}

func openLayers(base, scratch, nativeSource string, persist bool, maxBytes int64) (Layers, error) {
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
		layers.NativeSource = resolveNativeSource(nativeSource, scratchPath, "")
		return layers, nil
	}
	basePath, err := filepath.Abs(base)
	if err != nil {
		return Layers{}, fmt.Errorf("goatest: resolve build cache base: %w", err)
	}
	layers.Base = Layer{Dir: basePath}
	layers.NativeSource = resolveNativeSource(nativeSource, scratchPath, basePath)
	if persist {
		if err := layers.Base.ensureWithHooks(layerHooks{}); err != nil {
			return Layers{}, err
		}
	}
	return layers, nil
}

type ProgramOptions struct {
	Executable   string
	Base         string
	Scratch      string
	NativeSource string
	Persist      bool
	MaxBytes     int64
}

func Program(options ProgramOptions) (string, error) {
	arguments := []string{options.Executable, "cacheprog", "--base", options.Base, "--scratch", options.Scratch}
	if options.NativeSource != "" {
		arguments = append(arguments, "--native-source", options.NativeSource)
	}
	if options.Persist {
		arguments = append(arguments, "--persist")
	}
	if options.MaxBytes > 0 {
		arguments = append(arguments, "--max-bytes", strconv.FormatInt(options.MaxBytes, decimalRadix))
	}
	return joinQuoted(arguments)
}

func resolveNativeSource(source string, excluded ...string) string {
	if source == "" {
		return ""
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return ""
	}
	for _, path := range excluded {
		if path == "" {
			continue
		}
		excludedPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if canonical, err := filepath.EvalSymlinks(excludedPath); err == nil {
			excludedPath = canonical
		}
		if filepath.Clean(excludedPath) == filepath.Clean(resolved) {
			return ""
		}
	}
	return resolved
}

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

const DefaultBaseName = "build-v1"

func BaseDirectory(root, configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(root, filepath.FromSlash(configured))
}
