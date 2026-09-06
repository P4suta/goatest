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

type scanHooks struct {
	walk func(root string, visit fs.WalkDirFunc) error

	relative func(base, target string) (string, error)

	digestFile func(path string, mode fs.FileMode) (string, error)

	open func(path string) (io.ReadCloser, error)
}

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
		digestThrough := scanHooks{open: hooks.open}
		hooks.digestFile = func(path string, mode fs.FileMode) (string, error) {
			return fileDigestWithHooks(path, mode, digestThrough)
		}
	}
	return hooks
}

type graphHooks struct {
	marshalGraph func(value any, prefix, indent string) ([]byte, error)

	readGraph func(path string) ([]byte, error)

	unmarshalGraph func(data []byte, value any) error

	marshalRecord func(value any, prefix, indent string) ([]byte, error)

	mkdirAll func(path string, perm os.FileMode) error

	createTemporary func(directory, pattern string) (evidenceWritableFile, error)

	remove func(path string) error

	rename func(oldPath, newPath string) error
}

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

type mutationHooks struct {
	marshalStore func(value any, prefix, indent string) ([]byte, error)

	unmarshalStore func(data []byte, value any) error

	readStore func(path string) ([]byte, error)

	lstat func(path string) (os.FileInfo, error)

	mkdirAll func(path string, perm os.FileMode) error

	createTemporary func(directory, pattern string) (evidenceWritableFile, error)

	remove func(path string) error

	rename func(oldPath, newPath string) error
}

func (hooks mutationHooks) resolved() mutationHooks {
	if hooks.marshalStore == nil {
		hooks.marshalStore = json.MarshalIndent
	}
	if hooks.unmarshalStore == nil {
		hooks.unmarshalStore = json.Unmarshal
	}
	if hooks.readStore == nil {
		hooks.readStore = os.ReadFile
	}
	if hooks.lstat == nil {
		hooks.lstat = os.Lstat
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
