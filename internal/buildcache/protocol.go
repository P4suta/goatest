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

// The commands this program answers, exactly as cmd/go/internal/cacheprog
// names them. A go command only sends what the opening response advertises,
// which is what lets the protocol grow without breaking either side.
const (
	commandGet   = "get"
	commandPut   = "put"
	commandClose = "close"
)

// Stats is what one served go process asked of the cache. It is diagnostic
// exhaust, never evidence: a run reports it and decides nothing by it.
type Stats struct {
	Gets        int64 `json:"gets"`
	HitsScratch int64 `json:"hits_scratch"`
	HitsBase    int64 `json:"hits_base"`
	Misses      int64 `json:"misses"`
	Puts        int64 `json:"puts"`
	PutBytes    int64 `json:"put_bytes"`
	PrunedBytes int64 `json:"pruned_bytes"`
}

// Add accumulates another process's statistics into these.
func (stats *Stats) Add(other Stats) {
	stats.Gets += other.Gets
	stats.HitsScratch += other.HitsScratch
	stats.HitsBase += other.HitsBase
	stats.Misses += other.Misses
	stats.Puts += other.Puts
	stats.PutBytes += other.PutBytes
	stats.PrunedBytes += other.PrunedBytes
}

// Detail renders the statistics as the free text of a progress note.
func (stats Stats) Detail() string {
	return fmt.Sprintf("gets=%d hits-scratch=%d hits-base=%d misses=%d puts=%d put-bytes=%d pruned-bytes=%d",
		stats.Gets, stats.HitsScratch, stats.HitsBase, stats.Misses, stats.Puts, stats.PutBytes, stats.PrunedBytes)
}

// request is one message the go command sends. It mirrors
// cmd/go/internal/cacheprog.Request, which is internal to the toolchain and
// cannot be imported; the field names are the wire format and must not drift.
type request struct {
	ID       int64
	Command  string
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
}

// response is one message this program sends back, mirroring
// cmd/go/internal/cacheprog.Response for the same reason.
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

// Serve answers one go command's cache requests until it closes its end.
//
// The opening response advertises what this program can do, and the go command
// sends nothing else. Requests are answered in order, which the protocol
// allows, and a store that fails answers that one request with an error rather
// than ending the process: a build cache must never be the reason a build
// fails.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, layers Layers, stats *Stats) error {
	return serveWithHooks(ctx, stdin, stdout, layers, stats, serveHooks{})
}

// serveWithHooks is Serve against a clock and a filesystem the caller supplies.
func serveWithHooks(ctx context.Context, stdin io.Reader, stdout io.Writer, layers Layers, stats *Stats, hooks serveHooks) error {
	hooks = hooks.resolved()
	if stats == nil {
		stats = &Stats{}
	}
	reader := bufio.NewReaderSize(stdin, 64<<10)
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
			// The go command closes its end after the reply. Draining until it
			// does keeps the process alive for the file the last response
			// named, which the go command is about to open.
			_, _ = io.Copy(io.Discard, reader)
			return nil
		}
	}
}

// serveGet answers one lookup.
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
	default:
		stats.Misses++
		return response{ID: message.ID, Miss: true}
	}
	stored := entry.Time
	return response{
		ID: message.ID, OutputID: entry.OutputID, Size: entry.Size, Time: &stored, DiskPath: entry.DiskPath,
	}
}

// servePut stores one output. The body is streamed straight into the layer, so
// an output of any size costs one buffer rather than its own length, and the
// stream is always drained to the end of the quoted value even when the store
// refused it: the next request follows it on the same pipe.
func servePut(reader *bufio.Reader, layers Layers, message request, stats *Stats, hooks serveHooks) (response, error) {
	if message.BodySize <= 0 {
		entry, err := layers.putWithHooks(message.ActionID, message.OutputID, bytes.NewReader(nil), 0, hooks.now(), hooks.layer)
		return putResponse(message, entry, err, 0, stats), nil
	}
	body, err := openQuoted(reader)
	if err != nil {
		return response{}, err
	}
	counted := &countingReader{reader: base64.NewDecoder(base64.StdEncoding, body)}
	entry, putErr := layers.putWithHooks(message.ActionID, message.OutputID, counted, message.BodySize, hooks.now(), hooks.layer)
	if _, err := io.Copy(io.Discard, counted); err != nil && putErr == nil {
		putErr = fmt.Errorf("goatest: decode cacheprog body: %w", err)
	}
	if err := body.drain(); err != nil {
		return response{}, err
	}
	if putErr == nil && counted.read != message.BodySize {
		putErr = fmt.Errorf("goatest: cacheprog body is %d bytes, the go command declared %d", counted.read, message.BodySize)
	}
	return putResponse(message, entry, putErr, message.BodySize, stats), nil
}

// putResponse renders the outcome of a store and accounts for it.
func putResponse(message request, entry Entry, err error, size int64, stats *Stats) response {
	if err != nil {
		return response{ID: message.ID, Err: err.Error()}
	}
	stats.Puts++
	stats.PutBytes += size
	return response{ID: message.ID, DiskPath: entry.DiskPath}
}

// serveClose records what this process asked for and prunes the run's scratch
// layer of everything an hour of the run has not read.
//
// Only scratch is pruned here. The base layer belongs to the machine rather
// than to one go process, and it is collected once, under a lock, by the run
// that owns it.
func serveClose(layers Layers, stats *Stats, hooks serveHooks) {
	collected, err := layers.Scratch.collectWithHooks(
		Policy{TTL: touchInterval, MinIdle: touchInterval}, hooks.now(), hooks.layer)
	if err == nil {
		stats.PrunedBytes += collected.RemovedBytes
	}
	// Neither the pruning nor the record is worth failing a build over: the go
	// command treats an error on close as its own failure.
	_ = writeStats(layers.Scratch.Dir, hooks.statsName(), *stats, hooks.layer)
}

// readRequestLine reads one JSON request. The go command writes a newline of
// its own after each encoded value, so blank lines separate requests from the
// bodies that follow them and are skipped here.
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

// quotedReader reads the base64 characters of a JSON string value without
// holding the value in memory: a compiled package is megabytes, and the whole
// point of streaming it is not to buffer it twice.
type quotedReader struct {
	reader  *bufio.Reader
	pending []byte
	offset  int
	ended   bool
}

// openQuoted consumes the whitespace and the opening quote before a body.
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

// Read returns the next characters of the quoted value, stopping at the
// closing quote. Base64 never contains one, so the first quote ends the value.
func (body *quotedReader) Read(destination []byte) (int, error) {
	for body.offset >= len(body.pending) {
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
			// A stream that ended inside the value ended early. Saying so as
			// anything io.EOF matches would read as a body that simply
			// finished, which is the one thing it is not.
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return 0, fmt.Errorf("read cacheprog body: %w", err)
		}
		body.pending = append(body.pending[:0], chunk...)
		body.offset = 0
	}
	read := copy(destination, body.pending[body.offset:])
	body.offset += read
	return read, nil
}

// drain reads to the closing quote, so that the next request is the next thing
// on the stream however much of the body its store consumed.
func (body *quotedReader) drain() error {
	buffer := make([]byte, 32<<10)
	for {
		_, err := body.Read(buffer)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("goatest: %w", err)
		}
	}
}

// countingReader counts what a store actually took from a body, which is how a
// declared length is held to the bytes that arrived.
type countingReader struct {
	reader io.Reader
	read   int64
}

func (counter *countingReader) Read(destination []byte) (int, error) {
	read, err := counter.reader.Read(destination)
	counter.read += int64(read)
	return read, err
}

// defaultStatsName names one served process's statistics file. The process and
// the moment it closed are together unique among the go processes one run's
// scratch layer serves, including the ones a mutant's tests started.
func defaultStatsName() string {
	return fmt.Sprintf("%d-%d.json", os.Getpid(), time.Now().UnixNano())
}

// writeStats records what one served process asked for, beside the layer it
// asked it of, for the run to sum when it ends.
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

// Summarize sums what every go process a run served asked of the cache. A
// scratch layer that was never served, and one whose directory is already
// gone, summed to nothing rather than to an error: the run reports the total
// and decides nothing by it.
func Summarize(scratch string) (Stats, error) { return summarizeWithHooks(scratch, layerHooks{}) }

// summarizeWithHooks is Summarize against a filesystem the caller supplies.
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
			// A record a process did not finish writing is one process's
			// account of itself, not the run's verdict about anything.
			continue
		}
		total.Add(stats)
	}
	return total, nil
}
