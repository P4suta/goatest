// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"io"
	"io/fs"
	"os"
	"time"
)

// layerWritableFile is the temporary file a layer writes an object or an
// action into before renaming it into place.
type layerWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// layerHooks is the filesystem a layer is read and written through. Its zero
// value is the os package, so production passes it unset and a test fills in
// only the operation it drives, keeping the real behaviour for the rest.
//
// The hooks travel as an argument rather than as package-level variables. That
// is what lets the build cache tests run in parallel: what one test installs
// is reachable only from the call it passed it to.
type layerHooks struct {
	// mkdirAll creates a layer directory and the two-hex directory an entry
	// is written in.
	mkdirAll func(path string, perm os.FileMode) error
	// createTemporary opens the file an object or an action is written to
	// before it is renamed into place.
	createTemporary func(directory, pattern string) (layerWritableFile, error)
	// copyBody streams a put body into the temporary object file.
	copyBody func(destination io.Writer, source io.Reader) (int64, error)
	// readFile reads one action line.
	readFile func(path string) ([]byte, error)
	// stat reads the size of an object and the last-read time of an action.
	stat func(path string) (fs.FileInfo, error)
	// readDir enumerates one level of a layer.
	readDir func(path string) ([]os.DirEntry, error)
	// chtimes marks an action as read, which is the layer's LRU clock.
	chtimes func(path string, accessed, modified time.Time) error
	// remove deletes a temporary file, a collected entry, or the destination
	// a rename refused.
	remove func(path string) error
	// rename publishes a written temporary file as the stored entry.
	rename func(oldPath, newPath string) error
	// now is the clock the age of a leftover temporary file is measured
	// against, which is what tells a crash apart from a Prepare in flight.
	now func() time.Time
}

// resolved returns the hooks with every unset operation filled in from the os,
// io, and time packages.
func (hooks layerHooks) resolved() layerHooks {
	if hooks.mkdirAll == nil {
		hooks.mkdirAll = os.MkdirAll
	}
	if hooks.createTemporary == nil {
		hooks.createTemporary = func(directory, pattern string) (layerWritableFile, error) {
			return os.CreateTemp(directory, pattern)
		}
	}
	if hooks.copyBody == nil {
		hooks.copyBody = io.Copy
	}
	if hooks.readFile == nil {
		hooks.readFile = os.ReadFile
	}
	if hooks.stat == nil {
		hooks.stat = func(path string) (fs.FileInfo, error) { return os.Stat(path) }
	}
	if hooks.readDir == nil {
		hooks.readDir = os.ReadDir
	}
	if hooks.chtimes == nil {
		hooks.chtimes = os.Chtimes
	}
	if hooks.remove == nil {
		hooks.remove = os.Remove
	}
	if hooks.rename == nil {
		hooks.rename = os.Rename
	}
	if hooks.now == nil {
		hooks.now = time.Now
	}
	return hooks
}

// serveHooks is what the protocol loop reads outside its own streams: the
// clock the entries it stores are timed by, and the name of the statistics
// file one served go process leaves behind. Same argument-passing contract as
// layerHooks.
type serveHooks struct {
	// now is the clock a stored entry is timed by and the LRU clock a read
	// entry is touched against.
	now func() time.Time
	// statsName names the statistics file of this served go process. It must
	// be unique among the processes one scratch layer serves.
	statsName func() string
	// layer is the filesystem the layers are read and written through.
	layer layerHooks
}

// resolved returns the hooks with every unset operation filled in. The default
// statistics name is the process and the moment it closed, which is unique
// among the go processes one run's scratch layer serves.
func (hooks serveHooks) resolved() serveHooks {
	if hooks.now == nil {
		hooks.now = time.Now
	}
	if hooks.statsName == nil {
		hooks.statsName = defaultStatsName
	}
	hooks.layer = hooks.layer.resolved()
	return hooks
}
