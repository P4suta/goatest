// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/buildcache"
)

func TestProgramRendersACommandLineTheGoCommandCanSplit(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		program string
		base    string
		scratch string
		persist bool
		want    string
		wantErr bool
	}{
		{
			name: "plain paths", program: "/usr/bin/goatest", base: "/cache/base", scratch: "/tmp/scratch",
			want: "/usr/bin/goatest cacheprog --base /cache/base --scratch /tmp/scratch",
		},
		{
			name: "persisting", program: "/usr/bin/goatest", base: "/cache/base", scratch: "/tmp/scratch", persist: true,
			want: "/usr/bin/goatest cacheprog --base /cache/base --scratch /tmp/scratch --persist",
		},
		{
			name: "a space in the program path", program: "/home/a b/goatest", base: "/cache/base", scratch: "/tmp/s",
			want: "'/home/a b/goatest' cacheprog --base /cache/base --scratch /tmp/s",
		},
		{
			name: "a space in a layer path", program: "/usr/bin/goatest", base: "/c/my cache", scratch: "/tmp/s",
			want: "/usr/bin/goatest cacheprog --base '/c/my cache' --scratch /tmp/s",
		},
		{
			name: "a single quote in a path", program: "/usr/bin/goatest", base: "/c/o'brien", scratch: "/tmp/s",
			want: `/usr/bin/goatest cacheprog --base "/c/o'brien" --scratch /tmp/s`,
		},
		{
			name: "a double quote in a path", program: "/usr/bin/goatest", base: `/c/say"what`, scratch: "/tmp/s",
			want: `/usr/bin/goatest cacheprog --base '/c/say"what' --scratch /tmp/s`,
		},
		{
			name: "both kinds of quote in a path", program: "/usr/bin/goatest", base: `/c/o'say"what`, scratch: "/tmp/s",
			wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := buildcache.Program(testCase.program, testCase.base, testCase.scratch, testCase.persist)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("Program = %q, want a refusal", rendered)
				}
				return
			}
			if err != nil || rendered != testCase.want {
				t.Fatalf("Program = (%q, %v), want %q", rendered, err, testCase.want)
			}
		})
	}
}

func TestMainRefusesAnInvocationItCannotServe(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "unknown flag", arguments: []string{"--nonesuch"}, want: "Usage: goatest cacheprog"},
		{name: "no scratch layer", arguments: []string{"--base", "base"}, want: "requires --scratch"},
		{name: "persisting with no base layer", arguments: []string{"--scratch", "scratch", "--persist"}, want: "requires --base"},
		{name: "a positional argument", arguments: []string{"--scratch", "scratch", "serve"}, want: "takes no arguments"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			exit := buildcache.Main(testCase.arguments, strings.NewReader(""), &stdout, &stderr)
			if exit != 2 {
				t.Fatalf("Main exit = %d, want 2", exit)
			}
			if !strings.Contains(stderr.String(), testCase.want) {
				t.Fatalf("stderr = %q, want it to mention %q", stderr.String(), testCase.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing on the protocol stream", stdout.String())
			}
		})
	}
}

func TestMainRefusesALayerItCannotCreate(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := buildcache.Main([]string{"--scratch", filepath.Join(file, "scratch")}, strings.NewReader(""), &stdout, &stderr)
	if exit != 2 || !strings.Contains(stderr.String(), "goatest:") {
		t.Fatalf("Main exit = %d, stderr = %q", exit, stderr.String())
	}
}

func TestMainServesTheProtocolFromTheDirectoriesItWasGiven(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "base")
	scratch := filepath.Join(root, "scratch")
	content := "compiled bytes"
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: int64(len(content))},
		content,
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
		protocolRequest{ID: 3, Command: "close"},
	)
	var stdout, stderr bytes.Buffer
	if exit := buildcache.Main([]string{"--base", base, "--scratch", scratch, "--persist"}, stream, &stdout, &stderr); exit != 0 {
		t.Fatalf("Main exit = %d, stderr = %q", exit, stderr.String())
	}
	decoded := responses(t, stdout.Bytes())
	if len(decoded) != 4 || decoded[2].Miss || decoded[2].Size != int64(len(content)) {
		t.Fatalf("responses = %+v", decoded)
	}
	if !strings.HasPrefix(decoded[2].DiskPath, base) {
		t.Fatalf("served path = %q, want it inside the base layer %q", decoded[2].DiskPath, base)
	}
	if keys := files(t, buildcache.Layer{Dir: base}, "actions"); len(keys) != 1 {
		t.Fatalf("base actions = %v, want the one --persist stored", keys)
	}
	summed, err := buildcache.Summarize(scratch)
	if err != nil || summed.Puts != 1 {
		t.Fatalf("Summarize = (%+v, %v), want the served process's record", summed, err)
	}
}

func TestMainReportsAStreamItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	exit := buildcache.Main(
		[]string{"--scratch", filepath.Join(root, "scratch")}, strings.NewReader("{not json}\n"), &stdout, &stderr)
	if exit != 2 || !strings.Contains(stderr.String(), "goatest:") {
		t.Fatalf("Main exit = %d, stderr = %q", exit, stderr.String())
	}
}

func TestBaseDirectoryPrefersWhatTheProjectConfigured(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "repo")
	fallback := filepath.Join(string(filepath.Separator), "home", "cache", "goatest", "build-v1")
	if got := buildcache.BaseDirectory(root, "", fallback); got != fallback {
		t.Fatalf("BaseDirectory = %q, want the machine's default %q", got, fallback)
	}
	if got, want := buildcache.BaseDirectory(root, ".goatest/build", fallback), filepath.Join(root, ".goatest", "build"); got != want {
		t.Fatalf("BaseDirectory = %q, want %q", got, want)
	}
	absolute := filepath.Join(string(filepath.Separator), "elsewhere", "build")
	if got := buildcache.BaseDirectory(root, absolute, fallback); got != absolute {
		t.Fatalf("BaseDirectory = %q, want %q", got, absolute)
	}
}
