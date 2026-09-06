// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"io"
	"time"
)

type Source int

const (
	SourceNone Source = iota

	SourceScratch

	SourceBase

	SourceNative
)

func (source Source) String() string {
	switch source {
	case SourceScratch:
		return "scratch"
	case SourceBase:
		return "base"
	case SourceNative:
		return "native"
	default:
		return "none"
	}
}

type Layers struct {
	Scratch      Layer
	Base         Layer
	NativeSource string

	Persist bool

	MaxBytes int64
}

func (layers Layers) Get(actionID []byte, now time.Time) (Entry, Source, error) {
	return layers.getWithHooks(actionID, now, layerHooks{})
}

func (layers Layers) getWithHooks(actionID []byte, now time.Time, hooks layerHooks) (Entry, Source, error) {
	hooks = hooks.resolved()
	for _, source := range []Source{SourceScratch, SourceBase} {
		layer := layers.layer(source)
		record, modified, found, err := layer.readAction(actionID, hooks)
		if err != nil {
			return Entry{}, SourceNone, err
		}
		if !found {
			continue
		}
		outputID, err := hex.DecodeString(record.Output)
		if err != nil || len(outputID) == 0 {
			continue
		}
		for _, holder := range []Source{SourceScratch, SourceBase} {
			path, size, found, err := layers.layer(holder).object(outputID, hooks)
			if err != nil {
				return Entry{}, SourceNone, err
			}
			if !found || size != record.Size {
				continue
			}
			layer.touch(actionID, modified, now, hooks)
			return Entry{OutputID: outputID, Size: size, Time: record.Time, DiskPath: path}, source, nil
		}
	}
	entry, found, err := layers.importNative(actionID, now, hooks)
	if err != nil {
		return Entry{}, SourceNone, err
	}
	if found {
		return entry, SourceNative, nil
	}
	return Entry{}, SourceNone, nil
}

func (layers Layers) Put(actionID, outputID []byte, body io.Reader, size int64, now time.Time) (Entry, error) {
	return layers.putWithHooks(actionID, outputID, body, size, now, layerHooks{})
}

func (layers Layers) putWithHooks(actionID, outputID []byte, body io.Reader, size int64, now time.Time, hooks layerHooks) (Entry, error) {
	hooks = hooks.resolved()
	target := layers.target()
	for _, holder := range layers.holders() {
		path, stored, found, err := layers.layer(holder).object(outputID, hooks)
		if err != nil {
			return Entry{}, err
		}
		if !found || stored != size {
			continue
		}
		return target.putAction(actionID, outputID, size, now, path, hooks)
	}
	return target.putWithHooks(actionID, outputID, body, size, now, hooks)
}

func (layers Layers) target() Layer {
	if layers.Persist {
		return layers.Base
	}
	return layers.Scratch
}

func (layers Layers) holders() []Source {
	if layers.Persist {
		return []Source{SourceBase}
	}
	return []Source{SourceScratch, SourceBase}
}

func (layers Layers) layer(source Source) Layer {
	if source == SourceBase {
		return layers.Base
	}
	return layers.Scratch
}
