// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type progressEvent struct {
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type jsonl struct {
	mutex   sync.Mutex
	writer  io.Writer
	now     func() time.Time
	started time.Time
}

func NewJSONL(writer io.Writer, now func() time.Time) Notes {
	if now == nil {
		now = time.Now
	}
	return &jsonl{writer: writer, now: now, started: now()}
}

func (renderer *jsonl) Note(kind, detail string) {
	renderer.mutex.Lock()
	defer renderer.mutex.Unlock()

	data, _ := json.Marshal(progressEvent{
		Type: "progress", Kind: kind, Detail: detail,
		ElapsedMS: max(0, renderer.now().Sub(renderer.started).Milliseconds()),
	})
	_, _ = renderer.writer.Write(append(data, '\n'))
}

func (renderer *jsonl) Close() {}
