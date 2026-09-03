// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"io"
	"time"
)

// Source names the layer a read was answered from.
type Source int

const (
	// SourceNone is a miss.
	SourceNone Source = iota
	// SourceScratch is the layer this run will remove when it ends.
	SourceScratch
	// SourceBase is the layer this machine keeps.
	SourceBase
)

// String renders a source for a progress note.
func (source Source) String() string {
	switch source {
	case SourceScratch:
		return "scratch"
	case SourceBase:
		return "base"
	default:
		return "none"
	}
}

// Layers is the two-layer cache one served go process reads and writes.
//
// Reads resolve scratch and then base, so a run always sees its own work first
// and falls back on what the machine already compiled. Writes go to whichever
// layer Persist names, which is how the commands goatest issues itself grow
// the machine's standard library and dependencies while everything a run's
// mutants and tests compile stays in the scratch this run will remove.
type Layers struct {
	Scratch Layer
	Base    Layer
	// Persist sends writes to the base layer instead of the scratch layer.
	Persist bool
	// MaxBytes bounds the scratch layer, which the served process prunes as the
	// run goes. It travels on that process's command line because the process
	// reads no configuration: the run that started it is the only thing that
	// knows what the project allowed. Zero is unbounded.
	MaxBytes int64
}

// Get resolves one cache key. The key is looked for in scratch and then in
// base, and the output the winning key names is looked for the same way,
// because a scratch key may name an object the base layer already holds.
func (layers Layers) Get(actionID []byte, now time.Time) (Entry, Source, error) {
	return layers.getWithHooks(actionID, now, layerHooks{})
}

// getWithHooks is Get against a filesystem the caller supplies.
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
	return Entry{}, SourceNone, nil
}

// Put stores one output under one cache key. An output that is already stored
// where the key can still reach it later is not written again: the key is
// recorded against the bytes that are there, because the output identifier is
// the content's own identity.
//
// A key written to base may only name an object in base. The scratch layer is
// removed when the run ends, so a base key naming a scratch object would
// survive the bytes it promises and answer every later read with a miss.
func (layers Layers) Put(actionID, outputID []byte, body io.Reader, size int64, now time.Time) (Entry, error) {
	return layers.putWithHooks(actionID, outputID, body, size, now, layerHooks{})
}

// putWithHooks is Put against a filesystem the caller supplies.
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

// target is the layer writes land in.
func (layers Layers) target() Layer {
	if layers.Persist {
		return layers.Base
	}
	return layers.Scratch
}

// holders lists the layers a write may reuse an already stored object from,
// nearest first.
func (layers Layers) holders() []Source {
	if layers.Persist {
		return []Source{SourceBase}
	}
	return []Source{SourceScratch, SourceBase}
}

// layer resolves a source to the layer it names.
func (layers Layers) layer(source Source) Layer {
	if source == SourceBase {
		return layers.Base
	}
	return layers.Scratch
}
