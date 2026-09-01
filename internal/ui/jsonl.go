// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// progressEvent is the one shape a streamed progress line has. Kind and detail
// are the same vocabulary a trace records under its own progress events;
// elapsed_ms is stamped by this renderer alone and is diagnostic, so nothing
// deterministic may depend on it.
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

// NewJSONL streams one JSON object per note, followed by a newline.
// json.Marshal escapes control characters, which is what keeps every event on
// one physical line however a run labels its notes. A nil clock reads the wall
// clock.
func NewJSONL(writer io.Writer, now func() time.Time) Notes {
	if now == nil {
		now = time.Now
	}
	return &jsonl{writer: writer, now: now, started: now()}
}

func (renderer *jsonl) Note(kind, detail string) {
	renderer.mutex.Lock()
	defer renderer.mutex.Unlock()
	// progressEvent contains only strings and an integer, so json.Marshal
	// cannot reject its value. Writer failures remain best-effort like every
	// other progress renderer.
	data, _ := json.Marshal(progressEvent{
		Type: "progress", Kind: kind, Detail: detail,
		ElapsedMS: max(0, renderer.now().Sub(renderer.started).Milliseconds()),
	})
	_, _ = renderer.writer.Write(append(data, '\n'))
}

func (renderer *jsonl) Close() {}
