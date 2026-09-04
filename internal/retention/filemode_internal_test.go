// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package retention

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// untypedEntry is a directory entry whose Type reports nothing, which is what
// a listing that could not learn the type of an entry looks like: a zero
// FileMode reads as a regular file to anything that trusts the type bits alone.
type untypedEntry struct{ info fs.FileInfo }

func (entry untypedEntry) Name() string               { return entry.info.Name() }
func (entry untypedEntry) IsDir() bool                { return false }
func (entry untypedEntry) Type() fs.FileMode          { return 0 }
func (entry untypedEntry) Info() (fs.FileInfo, error) { return entry.info, nil }

// TestFileModeMeasuresOnlyWhatInfoSaysIsARegularFile holds the flat-file store
// to the file's own metadata rather than to the type bits of its listing. The
// bits can be empty for a directory on a filesystem that does not report
// types, and a directory measured as a file would be removed as one — whole.
func TestFileModeMeasuresOnlyWhatInfoSaysIsARegularFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "not-a-file")
	if err := os.Mkdir(directory, 0o755); err != nil {
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
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
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
