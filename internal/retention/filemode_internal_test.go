// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package retention

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/goatest/internal/filemode"
)

type untypedEntry struct{ info fs.FileInfo }

func (entry untypedEntry) Name() string               { return entry.info.Name() }
func (entry untypedEntry) IsDir() bool                { return false }
func (entry untypedEntry) Type() fs.FileMode          { return 0 }
func (entry untypedEntry) Info() (fs.FileInfo, error) { return entry.info, nil }

func TestFileModeMeasuresOnlyWhatInfoSaysIsARegularFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "not-a-file")
	if err := os.Mkdir(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := childFile.measure(directory, untypedEntry{info: info}); err == nil || !strings.Contains(err.Error(), "not a confined file") {
		t.Fatalf("measure of a directory with empty type bits = %v, want it refused as not a confined file", err)
	}
	file := filepath.Join(root, "record.json")
	if err := os.WriteFile(file, []byte("{}"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	size, _, err := childFile.measure(file, untypedEntry{info: info})
	if err != nil || size != 2 {
		t.Fatalf("measure of a regular file = (%d, %v), want its two bytes", size, err)
	}
}
