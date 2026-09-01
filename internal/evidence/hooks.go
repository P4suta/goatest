// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// scanHooks is the filesystem a repository scan observes the tree through. Its
// zero value is the os package, so production passes it unset and a test fills
// in only the operation it drives, keeping the real behaviour for the rest.
//
// The hooks travel as an argument rather than as package-level variables. That
// is what lets the evidence tests run in parallel: what one test installs is
// reachable only from the call it passed it to.
type scanHooks struct {
	// walk enumerates the tree below a root.
	walk func(root string, visit fs.WalkDirFunc) error
	// relative reports a walked path relative to the scanned root.
	relative func(base, target string) (string, error)
	// digestFile hashes one walked file, identified by path and mode.
	digestFile func(path string, mode fs.FileMode) (string, error)
	// open reads one file's contents for the default digest.
	open func(path string) (io.ReadCloser, error)
}

// resolved returns the hooks with every unset operation filled in from the os
// package. The default digest reads through the resolved open hook, so a test
// that replaces only open still drives the digest a scan computes.
func (hooks scanHooks) resolved() scanHooks {
	if hooks.walk == nil {
		hooks.walk = filepath.WalkDir
	}
	if hooks.relative == nil {
		hooks.relative = filepath.Rel
	}
	if hooks.open == nil {
		hooks.open = func(path string) (io.ReadCloser, error) { return os.Open(path) }
	}
	if hooks.digestFile == nil {
		// Snapshot what the default digest needs, so the closure never reads
		// the field it is about to fill.
		digestThrough := scanHooks{open: hooks.open}
		hooks.digestFile = func(path string, mode fs.FileMode) (string, error) {
			return fileDigestWithHooks(path, mode, digestThrough)
		}
	}
	return hooks
}

// graphHooks is the filesystem and the encoder an evidence graph is loaded and
// stored through. Its zero value is the os and encoding/json packages, with
// the same argument-passing contract as scanHooks.
type graphHooks struct {
	// marshalGraph encodes a canonical graph.
	marshalGraph func(value any, prefix, indent string) ([]byte, error)
	// readGraph reads a stored graph record.
	readGraph func(path string) ([]byte, error)
	// unmarshalGraph decodes the graph a record carries back into a value.
	unmarshalGraph func(data []byte, value any) error
	// marshalRecord encodes the record that wraps a graph.
	marshalRecord func(value any, prefix, indent string) ([]byte, error)
	// mkdirAll creates the directory a record is stored in.
	mkdirAll func(path string, perm os.FileMode) error
	// createTemporary opens the temporary file a record is written to before it
	// replaces the stored one.
	createTemporary func(directory, pattern string) (evidenceWritableFile, error)
	// remove deletes a temporary file, or the destination a rename refused.
	remove func(path string) error
	// rename publishes a written temporary file as the stored record.
	rename func(oldPath, newPath string) error
}

// resolved returns the hooks with every unset operation filled in from the os
// and encoding/json packages.
func (hooks graphHooks) resolved() graphHooks {
	if hooks.marshalGraph == nil {
		hooks.marshalGraph = json.MarshalIndent
	}
	if hooks.readGraph == nil {
		hooks.readGraph = os.ReadFile
	}
	if hooks.unmarshalGraph == nil {
		hooks.unmarshalGraph = json.Unmarshal
	}
	if hooks.marshalRecord == nil {
		hooks.marshalRecord = json.MarshalIndent
	}
	if hooks.mkdirAll == nil {
		hooks.mkdirAll = os.MkdirAll
	}
	if hooks.createTemporary == nil {
		hooks.createTemporary = func(directory, pattern string) (evidenceWritableFile, error) {
			return os.CreateTemp(directory, pattern)
		}
	}
	if hooks.remove == nil {
		hooks.remove = os.Remove
	}
	if hooks.rename == nil {
		hooks.rename = os.Rename
	}
	return hooks
}
