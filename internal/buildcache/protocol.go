// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	commandGet   = "get"
	commandPut   = "put"
	commandClose = "close"

	requestReadBufferSize = 64 << 10
	bodyDrainBufferSize   = 32 << 10
)

type Stats struct {
	Gets        int64 `json:"gets"`
	HitsScratch int64 `json:"hits_scratch"`
	HitsBase    int64 `json:"hits_base"`
	HitsNative  int64 `json:"hits_native"`
	NativeBytes int64 `json:"native_bytes"`
	Misses      int64 `json:"misses"`
	Puts        int64 `json:"puts"`
	PutBytes    int64 `json:"put_bytes"`
	PrunedBytes int64 `json:"pruned_bytes"`
}

func (stats *Stats) Add(other Stats) {
	stats.Gets += other.Gets
	stats.HitsScratch += other.HitsScratch
	stats.HitsBase += other.HitsBase
	stats.HitsNative += other.HitsNative
	stats.NativeBytes += other.NativeBytes
	stats.Misses += other.Misses
	stats.Puts += other.Puts
	stats.PutBytes += other.PutBytes
	stats.PrunedBytes += other.PrunedBytes
}

func (stats Stats) Detail() string {
	return fmt.Sprintf("gets=%d hits-scratch=%d hits-base=%d hits-native=%d native-bytes=%d misses=%d puts=%d put-bytes=%d pruned-bytes=%d",
		stats.Gets, stats.HitsScratch, stats.HitsBase, stats.HitsNative, stats.NativeBytes, stats.Misses, stats.Puts, stats.PutBytes, stats.PrunedBytes)
}

type request struct {
	ID       int64
	Command  string
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
}

type response struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []string   `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}

func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, layers Layers, stats *Stats) error {
	return serveWithHooks(ctx, stdin, stdout, layers, stats, serveHooks{})
}

func serveWithHooks(ctx context.Context, stdin io.Reader, stdout io.Writer, layers Layers, stats *Stats, hooks serveHooks) error {
	hooks = hooks.resolved()
	if stats == nil {
		stats = &Stats{}
	}
	reader := bufio.NewReaderSize(progressReader{reader: stdin}, requestReadBufferSize)
	writer := bufio.NewWriter(stdout)
	encoder := json.NewEncoder(writer)
	reply := func(message response) error {
		if err := encoder.Encode(message); err != nil {
			return fmt.Errorf("goatest: write cacheprog response: %w", err)
		}
		return writer.Flush()
	}
	if err := reply(response{KnownCommands: []string{commandGet, commandPut, commandClose}}); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readRequestLine(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var message request
		if err := json.Unmarshal(line, &message); err != nil {
			return fmt.Errorf("goatest: decode cacheprog request: %w", err)
		}
		answer := response{ID: message.ID}
		switch message.Command {
		case commandGet:
			answer = serveGet(layers, message, stats, hooks)
		case commandPut:
			answer, err = servePut(reader, layers, message, stats, hooks)
			if err != nil {
				return err
			}
		case commandClose:
			serveClose(layers, stats, hooks)
		default:
			answer.Err = fmt.Sprintf("goatest: cacheprog command %q is unsupported", message.Command)
		}
		if err := reply(answer); err != nil {
			return err
		}
		if message.Command == commandClose {
			_, _ = io.Copy(io.Discard, reader)
			return nil
		}
	}
}

func serveGet(layers Layers, message request, stats *Stats, hooks serveHooks) response {
	stats.Gets++
	entry, source, err := layers.getWithHooks(message.ActionID, hooks.now(), hooks.layer)
	if err != nil {
		stats.Misses++
		return response{ID: message.ID, Err: err.Error()}
	}
	switch source {
	case SourceScratch:
		stats.HitsScratch++
	case SourceBase:
		stats.HitsBase++
	case SourceNative:
		stats.HitsNative++
		stats.NativeBytes += entry.Size
	default:
		stats.Misses++
		return response{ID: message.ID, Miss: true}
	}
	stored := entry.Time
	return response{
		ID: message.ID, OutputID: entry.OutputID, Size: entry.Size, Time: &stored, DiskPath: entry.DiskPath,
	}
}

func servePut(reader *bufio.Reader, layers Layers, message request, stats *Stats, hooks serveHooks) (response, error) {
	if message.BodySize <= 0 {
		entry, err := layers.putWithHooks(message.ActionID, message.OutputID, bytes.NewReader(nil), 0, hooks.now(), hooks.layer)
		return putResponse(message, entry, err, 0, stats), nil
	}
	body, err := openQuoted(reader)
	if err != nil {
		return response{}, err
	}
	counter := &countingWriter{}
	decoded := io.TeeReader(base64.NewDecoder(base64.StdEncoding, progressReader{reader: body}), counter)
	entry, putErr := layers.putWithHooks(message.ActionID, message.OutputID, decoded, message.BodySize, hooks.now(), hooks.layer)
	if _, err := io.Copy(io.Discard, decoded); err != nil {
		return response{}, fmt.Errorf("goatest: decode cacheprog body: %w", err)
	}
	if err := body.drain(); err != nil {
		return response{}, err
	}
	if putErr == nil && counter.written != message.BodySize {
		putErr = fmt.Errorf("goatest: cacheprog body is %d bytes, the go command declared %d", counter.written, message.BodySize)
	}
	return putResponse(message, entry, putErr, message.BodySize, stats), nil
}

func putResponse(message request, entry Entry, err error, size int64, stats *Stats) response {
	if err != nil {
		return response{ID: message.ID, Err: err.Error()}
	}
	stats.Puts++
	stats.PutBytes += size
	return response{ID: message.ID, DiskPath: entry.DiskPath}
}

func serveClose(layers Layers, stats *Stats, hooks serveHooks) {
	collected, ran, err := layers.Scratch.collectLockedWithHooks(
		Policy{MaxBytes: layers.MaxBytes, MinIdle: layers.Scratch.MinIdle()},
		ScratchCollectInterval, hooks.now(), hooks.layer)
	if err == nil && ran {
		stats.PrunedBytes += collected.RemovedBytes
	}

	_ = writeStats(layers.Scratch.Dir, hooks.statsName(), *stats, hooks.layer)
}

func readRequestLine(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			return trimmed, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("goatest: read cacheprog request: %w", err)
		}
	}
}

type quotedReader struct {
	reader  *bufio.Reader
	pending []byte
	offset  int
	ended   bool
}

type progressReader struct {
	reader io.Reader
}

func (reader progressReader) Read(destination []byte) (int, error) {
	read, err := reader.reader.Read(destination)
	if len(destination) != 0 && read == 0 && err == nil {
		return 0, io.ErrNoProgress
	}
	return read, err
}

func openQuoted(reader *bufio.Reader) (*quotedReader, error) {
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("goatest: read cacheprog body: %w", err)
		}
		switch character {
		case ' ', '\t', '\r', '\n':
			continue
		case '"':
			return &quotedReader{reader: reader}, nil
		default:
			return nil, fmt.Errorf("goatest: cacheprog body opens with %q rather than a quoted string", character)
		}
	}
}

func (body *quotedReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if body.offset < len(body.pending) {
		read := copy(destination, body.pending[body.offset:])
		body.offset += read
		return read, nil
	}
	if body.ended {
		return 0, io.EOF
	}
	chunk, err := body.reader.ReadSlice('"')
	switch {
	case err == nil:
		body.ended = true
		chunk = chunk[:len(chunk)-1]
	case errors.Is(err, bufio.ErrBufferFull):
	default:
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, fmt.Errorf("read cacheprog body: %w", err)
	}
	body.pending = append(body.pending[:0], chunk...)
	body.offset = 0
	if len(body.pending) == 0 && body.ended {
		return 0, io.EOF
	}
	read := copy(destination, body.pending[body.offset:])
	body.offset += read
	return read, nil
}

func (body *quotedReader) drain() error {
	buffer := make([]byte, bodyDrainBufferSize)
	if _, err := io.CopyBuffer(io.Discard, progressReader{reader: body}, buffer); err != nil {
		return fmt.Errorf("goatest: %w", err)
	}
	return nil
}

type countingWriter struct {
	written int64
}

func (counter *countingWriter) Write(source []byte) (int, error) {
	counter.written += int64(len(source))
	return len(source), nil
}

func defaultStatsName() string {
	return fmt.Sprintf("%d-%d.json", os.Getpid(), time.Now().UnixNano())
}

func writeStats(scratch, name string, stats Stats, hooks layerHooks) error {
	if scratch == "" || name == "" {
		return nil
	}
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(scratch, statsDirectory, name), append(data, '\n'), ".stats-*.tmp", hooks.resolved())
}

func Summarize(scratch string) (Stats, error) { return summarizeWithHooks(scratch, layerHooks{}) }

func summarizeWithHooks(scratch string, hooks layerHooks) (Stats, error) {
	hooks = hooks.resolved()
	if scratch == "" {
		return Stats{}, nil
	}
	directory := filepath.Join(scratch, statsDirectory)
	entries, err := hooks.readDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, fmt.Errorf("goatest: read build cache statistics: %w", err)
	}
	var total Stats
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := hooks.readFile(filepath.Join(directory, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Stats{}, fmt.Errorf("goatest: read build cache statistics: %w", err)
		}
		var stats Stats
		if err := json.Unmarshal(data, &stats); err != nil {
			continue
		}
		total.Add(stats)
	}
	return total, nil
}
