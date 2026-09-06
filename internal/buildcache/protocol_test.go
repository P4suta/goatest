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

const (
	getPutGetResponseCount    = 4
	continuingResponseCount   = 3
	closedStreamResponseCount = 2
	largeProtocolBodyLines    = 8192
)

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

type terminalReader struct {
	reader   io.Reader
	terminal bool
}

func (reader *terminalReader) Read(destination []byte) (int, error) {
	if reader.terminal {
		panic("read after terminal stream result")
	}
	read, err := reader.reader.Read(destination)
	reader.terminal = read == 0 && err != nil
	return read, err
}

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

func served(t *testing.T, layers buildcache.Layers, stream io.Reader) ([]protocolResponse, buildcache.Stats, error) {
	t.Helper()
	var written bytes.Buffer
	var stats buildcache.Stats
	err := buildcache.Serve(t.Context(), &terminalReader{reader: stream}, &written, layers, &stats)
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
	if len(decoded) != getPutGetResponseCount {
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

func TestServeMeasuresVerifiedNativeImports(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	native := t.TempDir()
	action := identifier(7)
	body := "native compiled bytes"
	output := storeNativeCacheEntry(t, native, action, body)
	layers.NativeSource = native
	decoded, stats, err := served(t, layers, requestStream(t,
		protocolRequest{ID: 1, Command: "get", ActionID: action},
	))
	if err != nil || len(decoded) != closedStreamResponseCount {
		t.Fatalf("Serve = (%+v, %+v, %v)", decoded, stats, err)
	}
	hit := decoded[1]
	if hit.Miss || !bytes.Equal(hit.OutputID, output) || hit.Size != int64(len(body)) {
		t.Fatalf("native hit = %+v", hit)
	}
	want := buildcache.Stats{Gets: 1, HitsNative: 1, NativeBytes: int64(len(body))}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

func TestServeStreamsALargeBodyThroughTheQuotedValue(t *testing.T) {
	t.Parallel()
	layers := twoLayers(t, false)
	content := strings.Repeat("compiled package bytes\n", largeProtocolBodyLines)
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
	if len(decoded) != continuingResponseCount {
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
	if len(decoded) != continuingResponseCount {
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
	if len(decoded) != closedStreamResponseCount {
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
	stats := buildcache.Stats{Gets: 1, HitsScratch: 2, HitsBase: 3, HitsNative: 4, NativeBytes: 5, Misses: 6, Puts: 7, PutBytes: 8, PrunedBytes: 9}
	want := "gets=1 hits-scratch=2 hits-base=3 hits-native=4 native-bytes=5 misses=6 puts=7 put-bytes=8 pruned-bytes=9"
	if got := stats.Detail(); got != want {
		t.Fatalf("Detail = %q, want %q", got, want)
	}
	var total buildcache.Stats
	total.Add(stats)
	total.Add(stats)
	if total.Gets != 2 || total.PrunedBytes != 18 {
		t.Fatalf("Add = %+v, want both processes counted", total)
	}
}
