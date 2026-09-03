// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/buildcache"
)

// protocolRequest and protocolResponse mirror the messages
// cmd/go/internal/cacheprog defines. The test writes and reads the wire format
// itself, so that a change to what goatest serves has to keep answering the go
// command the go command actually is.
type protocolRequest struct {
	ID       int64
	Command  string
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
}

type protocolResponse struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []string   `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}

// requestStream builds the byte stream the go command writes: each request as
// one encoded JSON value, a newline of its own after it, and a put body as a
// base64 JSON string on the line that follows. cmd/go/internal/cache/prog.go
// writes exactly this, and a server that only parses what a test invented
// would not be a server for it.
func requestStream(t *testing.T, messages ...any) io.Reader {
	t.Helper()
	var stream bytes.Buffer
	encoder := json.NewEncoder(&stream)
	for _, message := range messages {
		switch typed := message.(type) {
		case protocolRequest:
			if err := encoder.Encode(typed); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			stream.WriteByte('\n')
		case string:
			stream.WriteByte('"')
			stream.WriteString(base64.StdEncoding.EncodeToString([]byte(typed)))
			stream.WriteString("\"\n")
		case []byte:
			stream.Write(typed)
		default:
			t.Fatalf("unsupported stream element %T", message)
		}
	}
	return bytes.NewReader(stream.Bytes())
}

// responses decodes everything the program wrote, in order.
func responses(t *testing.T, stream []byte) []protocolResponse {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(stream))
	var decoded []protocolResponse
	for {
		var message protocolResponse
		err := decoder.Decode(&message)
		if err == io.EOF {
			return decoded
		}
		if err != nil {
			t.Fatalf("decode response: %v (stream %q)", err, stream)
		}
		decoded = append(decoded, message)
	}
}

// served runs the protocol loop over one request stream.
func served(t *testing.T, layers buildcache.Layers, stream io.Reader) ([]protocolResponse, buildcache.Stats, error) {
	t.Helper()
	var written bytes.Buffer
	var stats buildcache.Stats
	err := buildcache.Serve(t.Context(), stream, &written, layers, &stats)
	return responses(t, written.Bytes()), stats, err
}

func TestServeAnnouncesWhatItCanDoBeforeAnythingElse(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	decoded, _, err := served(t, layers, strings.NewReader(""))
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 1 || decoded[0].ID != 0 {
		t.Fatalf("responses = %+v, want one opening message with ID 0", decoded)
	}
	if got := decoded[0].KnownCommands; len(got) != 3 || got[0] != "get" || got[1] != "put" || got[2] != "close" {
		t.Fatalf("KnownCommands = %v, want get, put, and close", got)
	}
}

func TestServeAnswersAMissAndThenAHit(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	content := "compiled bytes"
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "get", ActionID: identifier(1)},
		protocolRequest{ID: 2, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: int64(len(content))},
		content,
		protocolRequest{ID: 3, Command: "get", ActionID: identifier(1)},
	)
	decoded, stats, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 4 {
		t.Fatalf("responses = %+v, want the opening message and three answers", decoded)
	}
	if miss := decoded[1]; miss.ID != 1 || !miss.Miss || miss.Err != "" {
		t.Fatalf("first get = %+v, want a miss", miss)
	}
	stored := decoded[2]
	if stored.ID != 2 || stored.Err != "" || !filepath.IsAbs(stored.DiskPath) {
		t.Fatalf("put = %+v, want an absolute disk path", stored)
	}
	hit := decoded[3]
	if hit.ID != 3 || hit.Miss || !bytes.Equal(hit.OutputID, identifier(2)) || hit.Size != int64(len(content)) {
		t.Fatalf("second get = %+v, want a hit naming the stored output", hit)
	}
	if hit.Time == nil || hit.DiskPath != stored.DiskPath {
		t.Fatalf("second get = %+v, want the time and the path the put reported", hit)
	}
	data, err := os.ReadFile(hit.DiskPath)
	if err != nil || string(data) != content {
		t.Fatalf("served file = %q (%v), want %q", data, err, content)
	}
	want := buildcache.Stats{Gets: 2, HitsScratch: 1, Misses: 1, Puts: 1, PutBytes: int64(len(content))}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

func TestServeStreamsALargeBodyThroughTheQuotedValue(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	content := strings.Repeat("compiled package bytes\n", 8192)
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: int64(len(content))},
		content,
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
	)
	decoded, stats, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 3 || decoded[1].Err != "" || decoded[2].Size != int64(len(content)) {
		t.Fatalf("responses = %+v", decoded)
	}
	data, err := os.ReadFile(decoded[2].DiskPath)
	if err != nil || string(data) != content {
		t.Fatalf("served file is %d bytes (%v), want %d", len(data), err, len(content))
	}
	if stats.PutBytes != int64(len(content)) {
		t.Fatalf("stats = %+v, want %d put bytes", stats, len(content))
	}
}

func TestServeStoresAnOutputWithNoBody(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2)},
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
	)
	decoded, _, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 3 || decoded[1].Err != "" || decoded[2].Miss || decoded[2].Size != 0 {
		t.Fatalf("responses = %+v, want an empty output stored and served", decoded)
	}
}

func TestServeRefusesABodyThatIsNotTheDeclaredLength(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: 64},
		"short",
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
	)
	decoded, stats, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("responses = %+v, want the loop to have carried on", decoded)
	}
	if decoded[1].Err == "" || !strings.Contains(decoded[1].Err, "declared") {
		t.Fatalf("put = %+v, want an error naming the declared length", decoded[1])
	}
	if !decoded[2].Miss {
		t.Fatalf("get after the refused put = %+v, want a miss", decoded[2])
	}
	if stats.Puts != 0 {
		t.Fatalf("stats = %+v, want no put counted", stats)
	}
}

func TestServeReportsAnUnsupportedCommandAndKeepsServing(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "get2", ActionID: identifier(1)},
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
	)
	decoded, _, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("responses = %+v, want the loop to have carried on", decoded)
	}
	if decoded[1].ID != 1 || !strings.Contains(decoded[1].Err, "get2") {
		t.Fatalf("unsupported command = %+v, want an error naming it", decoded[1])
	}
	if decoded[2].ID != 2 || !decoded[2].Miss {
		t.Fatalf("following get = %+v, want the loop still answering", decoded[2])
	}
}

func TestServeRecordsWhatItServedWhenTheGoCommandCloses(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	content := "compiled bytes"
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: int64(len(content))},
		content,
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
		protocolRequest{ID: 3, Command: "close"},
	)
	decoded, stats, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 4 || decoded[3].ID != 3 || decoded[3].Err != "" {
		t.Fatalf("responses = %+v, want a plain answer to close", decoded)
	}
	summed, err := buildcache.Summarize(layers.Scratch.Dir)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summed != stats {
		t.Fatalf("recorded stats = %+v, served stats = %+v", summed, stats)
	}
	if summed.Puts != 1 || summed.HitsScratch != 1 {
		t.Fatalf("recorded stats = %+v, want one put and one scratch hit", summed)
	}
}

func TestServeDrivesTheLoopToTheEndOfTheStreamAfterClose(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	stream := requestStream(t,
		protocolRequest{ID: 1, Command: "close"},
		protocolRequest{ID: 2, Command: "get", ActionID: identifier(1)},
	)
	decoded, _, err := served(t, layers, stream)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("responses = %+v, want the opening message and the answer to close alone", decoded)
	}
}

func TestServeReportsAStreamItCannotRead(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		stream io.Reader
	}{
		{name: "malformed request", stream: strings.NewReader("{not json}\n")},
		{
			name: "body that is not a quoted string",
			stream: requestStream(t,
				protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: 4},
				[]byte("nope\n"),
			),
		},
		{
			name: "body that never ends",
			stream: requestStream(t,
				protocolRequest{ID: 1, Command: "put", ActionID: identifier(1), OutputID: identifier(2), BodySize: 4},
				[]byte("\"aGk=\n"),
			),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			layers := twoLayers(t, false)
			if _, _, err := served(t, layers, testCase.stream); err == nil {
				t.Fatal("Serve accepted a stream it cannot read")
			}
		})
	}
}

func TestSummarizeSumsEveryServedProcessAndForgivesAnAbsentLayer(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	for range 3 {
		stream := requestStream(t,
			protocolRequest{ID: 1, Command: "get", ActionID: identifier(1)},
			protocolRequest{ID: 2, Command: "close"},
		)
		if _, _, err := served(t, layers, stream); err != nil {
			t.Fatalf("Serve: %v", err)
		}
	}
	summed, err := buildcache.Summarize(layers.Scratch.Dir)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if summed.Gets != 3 || summed.Misses != 3 {
		t.Fatalf("summed = %+v, want three processes' worth", summed)
	}
	absent, err := buildcache.Summarize(filepath.Join(t.TempDir(), "gone"))
	if err != nil || absent != (buildcache.Stats{}) {
		t.Fatalf("Summarize of an absent layer = (%+v, %v)", absent, err)
	}
	if empty, err := buildcache.Summarize(""); err != nil || empty != (buildcache.Stats{}) {
		t.Fatalf("Summarize of no layer = (%+v, %v)", empty, err)
	}
}

func TestStatsDetailNamesEveryCounter(t *testing.T) {
	t.Parallel()
	stats := buildcache.Stats{Gets: 1, HitsScratch: 2, HitsBase: 3, Misses: 4, Puts: 5, PutBytes: 6, PrunedBytes: 7}
	want := "gets=1 hits-scratch=2 hits-base=3 misses=4 puts=5 put-bytes=6 pruned-bytes=7"
	if got := stats.Detail(); got != want {
		t.Fatalf("Detail = %q, want %q", got, want)
	}
	var total buildcache.Stats
	total.Add(stats)
	total.Add(stats)
	if total.Gets != 2 || total.PrunedBytes != 14 {
		t.Fatalf("Add = %+v, want both processes counted", total)
	}
}
