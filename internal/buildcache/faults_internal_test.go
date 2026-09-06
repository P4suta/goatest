// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/filemode"
)

const stubCopiedBytes = 7

var faultsMoment = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

type stubLayerFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
	written  []byte
}

func (file *stubLayerFile) Name() string { return file.name }

func (file *stubLayerFile) Write(data []byte) (int, error) {
	file.written = append(file.written, data...)
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *stubLayerFile) Sync() error  { return file.syncErr }
func (file *stubLayerFile) Close() error { return file.closeErr }

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

type stalledReader struct{}

func (stalledReader) Read([]byte) (int, error) { return 0, nil }

type stubLayerInfo struct {
	size     int64
	modified time.Time
}

func (info stubLayerInfo) Name() string       { return "stub" }
func (info stubLayerInfo) Size() int64        { return info.size }
func (info stubLayerInfo) Mode() fs.FileMode  { return filemode.ReadableFile }
func (info stubLayerInfo) ModTime() time.Time { return info.modified }
func (info stubLayerInfo) IsDir() bool        { return false }
func (info stubLayerInfo) Sys() any           { return nil }

func key(value byte) []byte {
	identifier := make([]byte, sha256.Size)
	for index := range identifier {
		identifier[index] = value
	}
	return identifier
}

func TestPutPropagatesEveryWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{
		"object-mkdir", "object-create", "object-copy", "object-sync", "object-close", "object-rename",
		"action-mkdir", "action-create", "action-write", "action-sync", "action-close", "action-rename",
	} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New(stage + " failure")
			object := strings.HasPrefix(stage, "object-")
			file := &stubLayerFile{name: filepath.Join(t.TempDir(), "temporary")}
			hooks := layerHooks{
				createTemporary: func(string, string) (layerWritableFile, error) { return file, nil },
				stat:            func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
				remove:          func(string) error { return nil },
				rename:          func(string, string) error { return nil },
				copyBody:        func(io.Writer, io.Reader) (int64, error) { return stubCopiedBytes, nil },
				mkdirAll:        func(string, os.FileMode) error { return nil },
			}
			switch stage {
			case "object-mkdir":
				hooks.mkdirAll = func(path string, _ os.FileMode) error {
					if strings.Contains(path, objectsDirectory) {
						return failure
					}
					return nil
				}
			case "action-mkdir":
				hooks.mkdirAll = func(path string, _ os.FileMode) error {
					if strings.Contains(path, actionsDirectory) {
						return failure
					}
					return nil
				}
			case "object-create", "action-create":
				created := 0
				hooks.createTemporary = func(string, string) (layerWritableFile, error) {
					created++
					if (object && created == 1) || (!object && created == 2) {
						return nil, failure
					}
					return file, nil
				}
			case "object-copy":
				hooks.copyBody = func(io.Writer, io.Reader) (int64, error) { return 0, failure }
			case "action-write":
				file.writeErr = failure
			case "object-sync", "action-sync":
				file.syncErr = failure
			case "object-close", "action-close":
				file.closeErr = failure
			case "object-rename", "action-rename":
				renamed := 0
				hooks.rename = func(string, string) error {
					renamed++

					if (object && renamed <= 2) || (!object && renamed >= 2) {
						return failure
					}
					return nil
				}
			}
			layer := Layer{Dir: t.TempDir()}
			_, err := layer.putWithHooks(key(1), key(2), strings.NewReader("content"), 7, faultsMoment, hooks)
			if !errors.Is(err, failure) {
				t.Fatalf("Put error = %v, want %v", err, failure)
			}
		})
	}
}

func TestPutReportsABodyThatGivesOut(t *testing.T) {
	t.Parallel()
	failure := errors.New("body failure")
	layer := Layer{Dir: t.TempDir()}
	if _, err := (Layers{Scratch: layer}).Put(key(1), key(2), failingReader{err: failure}, 7, faultsMoment); !errors.Is(err, failure) {
		t.Fatalf("Put error = %v, want %v", err, failure)
	}
	if _, source, err := (Layers{Scratch: layer}).Get(key(1), faultsMoment); source != SourceNone || err != nil {
		t.Fatalf("Get after the failed Put = (%s, %v), want a miss", source, err)
	}
}

func TestPrepareAndGetPropagateTheStagesTheyRunThrough(t *testing.T) {
	t.Parallel()
	failure := errors.New("filesystem failure")
	layer := Layer{Dir: t.TempDir()}
	if err := layer.prepareWithHooks(layerHooks{
		mkdirAll: func(string, os.FileMode) error { return failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Prepare error = %v, want %v", err, failure)
	}
	if err := layer.prepareWithHooks(layerHooks{
		createTemporary: func(string, string) (layerWritableFile, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Prepare error = %v, want %v", err, failure)
	}
	layers := Layers{Scratch: layer}
	if _, _, err := layers.getWithHooks(key(1), faultsMoment, layerHooks{
		stat: func(string) (fs.FileInfo, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Get error = %v, want %v", err, failure)
	}
	if _, _, err := layers.getWithHooks(key(1), faultsMoment, layerHooks{
		stat:     func(string) (fs.FileInfo, error) { return stubLayerInfo{size: 7, modified: faultsMoment}, nil },
		readFile: func(string) ([]byte, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Get error = %v, want %v", err, failure)
	}
}

func TestGetSurvivesARefreshItCannotWrite(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Layers{Scratch: layer}).Put(key(1), key(2), strings.NewReader("content"), 7, faultsMoment); err != nil {
		t.Fatal(err)
	}
	stale := faultsMoment.Add(-48 * time.Hour)
	if err := os.Chtimes(layer.actionPath(key(1)), stale, stale); err != nil {
		t.Fatal(err)
	}
	refreshed := 0
	entry, source, err := Layers{Scratch: layer}.getWithHooks(key(1), faultsMoment, layerHooks{
		chtimes: func(string, time.Time, time.Time) error {
			refreshed++
			return errors.New("refresh failure")
		},
	})
	if err != nil || source != SourceScratch || entry.Size != 7 {
		t.Fatalf("Get = (%+v, %s, %v), want a hit despite the refusal to refresh", entry, source, err)
	}
	if refreshed != 1 {
		t.Fatalf("refresh attempts = %d, want one", refreshed)
	}
}

func TestCollectAndInspectPropagateTheStagesTheyRunThrough(t *testing.T) {
	t.Parallel()
	failure := errors.New("filesystem failure")
	layer := Layer{Dir: t.TempDir()}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Layers{Scratch: layer}).Put(key(1), key(2), strings.NewReader("content"), 7, faultsMoment); err != nil {
		t.Fatal(err)
	}
	ageFaultsEntry(t, layer)
	if _, err := layer.collectWithHooks(Policy{TTL: time.Hour}, faultsMoment, layerHooks{
		readDir: func(string) ([]os.DirEntry, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Collect error = %v, want %v", err, failure)
	}
	if _, err := layer.inspectWithHooks(layerHooks{
		readDir: func(string) ([]os.DirEntry, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Inspect error = %v, want %v", err, failure)
	}
	if _, err := layer.collectWithHooks(Policy{TTL: time.Nanosecond}, faultsMoment.Add(time.Hour), layerHooks{
		remove: func(string) error { return failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Collect error = %v, want %v", err, failure)
	}
	if _, err := layer.collectWithHooks(Policy{}, faultsMoment, layerHooks{
		readFile: func(string) ([]byte, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Collect error = %v, want %v", err, failure)
	}
}

func TestCollectIgnoresWhatWasAlreadyGone(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Layers{Scratch: layer}).Put(key(1), key(2), strings.NewReader("content"), 7, faultsMoment); err != nil {
		t.Fatal(err)
	}
	ageFaultsEntry(t, layer)
	collected, err := layer.collectWithHooks(Policy{TTL: time.Nanosecond}, faultsMoment.Add(time.Hour), layerHooks{
		remove: func(string) error { return os.ErrNotExist },
	})
	if err != nil || collected.RemovedActions != 1 || collected.RemovedObjects != 1 {
		t.Fatalf("Collect = (%+v, %v), want a collection that forgave the missing files", collected, err)
	}
}

func TestSummarizePropagatesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	failure := errors.New("filesystem failure")
	scratch := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scratch, statsDirectory), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, statsDirectory, "1-1.json"), []byte(`{"gets":2}`), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, statsDirectory, "2-2.json"), []byte("{not json"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	summed, err := Summarize(scratch)
	if err != nil || summed.Gets != 2 {
		t.Fatalf("Summarize = (%+v, %v), want the record it could read", summed, err)
	}
	if _, err := summarizeWithHooks(scratch, layerHooks{
		readDir: func(string) ([]os.DirEntry, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Summarize error = %v, want %v", err, failure)
	}
	if _, err := summarizeWithHooks(scratch, layerHooks{
		readFile: func(string) ([]byte, error) { return nil, failure },
	}); !errors.Is(err, failure) {
		t.Fatalf("Summarize error = %v, want %v", err, failure)
	}
}

func TestCloseNeverFailsTheGoCommandOverItsOwnRecord(t *testing.T) {
	t.Parallel()
	failure := errors.New("filesystem failure")
	layers := Layers{Scratch: Layer{Dir: t.TempDir()}}
	if err := layers.Scratch.Prepare(); err != nil {
		t.Fatal(err)
	}
	var written strings.Builder
	stats := Stats{Gets: 1}
	err := serveWithHooks(t.Context(), strings.NewReader(`{"ID":1,"Command":"close"}`+"\n"), &written, layers, &stats,
		serveHooks{layer: layerHooks{
			readDir:         func(string) ([]os.DirEntry, error) { return nil, failure },
			createTemporary: func(string, string) (layerWritableFile, error) { return nil, failure },
		}})
	if err != nil {
		t.Fatalf("Serve error = %v, want a close that swallowed its own failures", err)
	}
	if !strings.Contains(written.String(), `{"ID":1}`) {
		t.Fatalf("responses = %q, want a plain answer to close", written.String())
	}
}

func TestServeReportsAStoreThatFailedAsThatRequestsError(t *testing.T) {
	t.Parallel()
	failure := errors.New("filesystem failure")
	layers := Layers{Scratch: Layer{Dir: t.TempDir()}}
	var written strings.Builder
	var stats Stats
	err := serveWithHooks(t.Context(), &terminalFaultReader{reader: strings.NewReader(`{"ID":1,"Command":"get","ActionID":"AQ=="}` + "\n")},
		&written, layers, &stats, serveHooks{layer: layerHooks{
			stat: func(string) (fs.FileInfo, error) { return nil, failure },
		}})
	if err != nil {
		t.Fatalf("Serve error = %v, want the loop to have survived the store", err)
	}
	if !strings.Contains(written.String(), failure.Error()) {
		t.Fatalf("responses = %q, want the failure reported on that request", written.String())
	}
	if stats.Misses != 1 {
		t.Fatalf("stats = %+v, want the failed lookup counted as a miss", stats)
	}
}

func TestQuotedReaderReturnsPendingBytesAndThenEnds(t *testing.T) {
	t.Parallel()
	want := []byte("body")
	body := &quotedReader{pending: slices.Clone(want), ended: true}
	destination := make([]byte, len(want))
	read, err := body.Read(destination)
	if err != nil || read != len(want) || !bytes.Equal(destination, want) {
		t.Fatalf("first Read = (%q, %d, %v), want (%q, %d, nil)", destination, read, err, want, len(want))
	}
	read, err = body.Read(destination)
	if read != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second Read = (%d, %v), want (0, EOF)", read, err)
	}
}

func TestProgressReaderRejectsOnlyAStalledNonemptyRead(t *testing.T) {
	t.Parallel()
	stalled := progressReader{reader: stalledReader{}}
	if read, err := stalled.Read(make([]byte, 1)); read != 0 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("stalled Read = (%d, %v), want (0, ErrNoProgress)", read, err)
	}
	if read, err := stalled.Read(nil); read != 0 || err != nil {
		t.Fatalf("empty Read = (%d, %v), want (0, nil)", read, err)
	}
	want := []byte("content")
	destination := make([]byte, len(want))
	read, err := (progressReader{reader: bytes.NewReader(want)}).Read(destination)
	if read != len(want) || err != nil || !bytes.Equal(destination, want) {
		t.Fatalf("progressing Read = (%q, %d, %v), want (%q, %d, nil)", destination, read, err, want, len(want))
	}
}

type terminalFaultReader struct {
	reader   io.Reader
	terminal bool
}

func (reader *terminalFaultReader) Read(destination []byte) (int, error) {
	if reader.terminal {
		panic("read after terminal stream result")
	}
	read, err := reader.reader.Read(destination)
	reader.terminal = read == 0 && err != nil
	return read, err
}

func TestServeReportsAResponseItCannotWrite(t *testing.T) {
	t.Parallel()
	failure := errors.New("stream failure")
	layers := Layers{Scratch: Layer{Dir: t.TempDir()}}
	var stats Stats
	err := Serve(t.Context(), strings.NewReader(""), failingWriter{err: failure}, layers, &stats)
	if !errors.Is(err, failure) {
		t.Fatalf("Serve error = %v, want %v", err, failure)
	}
}

func ageFaultsEntry(t *testing.T, layer Layer) {
	t.Helper()
	for _, path := range []string{layer.actionPath(key(1)), layer.objectPath(key(2))} {
		if err := os.Chtimes(path, faultsMoment, faultsMoment); err != nil {
			t.Fatal(err)
		}
	}
}

type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }
