// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mutationFixture is a store that passes every self-consistency rule, so a
// fault test fails for the fault it installed and nothing else.
func mutationFixture() MutationStore {
	digest := func(character string) string { return strings.Repeat(character, 64) }
	return MutationStore{
		ModulePath: "example/module",
		Records: []MutationRecord{{
			MutantID: digest("a"), Path: "value.go", Package: "example/module/pkg",
			Outcome: MutationOutcomeKilled, Provenance: "snapshot=" + digest("f"),
			KilledBy: &TargetKey{
				Package: "example/module/pkg", Name: "TestKills", Kind: "test", Key: digest("1"),
			},
		}},
	}
}

func TestSaveMutationPropagatesEverySerializationAndWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"validate", "marshal", "unmarshal", "mkdir", "create", "write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New(stage + " failure")
			file := &stubEvidenceFile{name: filepath.Join(root, "temporary")}
			creates := 0
			store := mutationFixture()
			hooks := mutationHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) {
					creates++
					return file, nil
				},
			}
			switch stage {
			case "validate":
				store.Records[0].Outcome = "flaky"
			case "marshal":
				hooks.marshalStore = func(any, string, string) ([]byte, error) { return nil, failure }
			case "unmarshal":
				hooks.unmarshalStore = func([]byte, any) error { return failure }
			case "mkdir":
				hooks.mkdirAll = func(string, os.FileMode) error { return failure }
			case "create":
				hooks.createTemporary = func(string, string) (evidenceWritableFile, error) {
					creates++
					return nil, failure
				}
			case "write":
				file.writeErr = failure
			case "sync":
				file.syncErr = failure
			case "close":
				file.closeErr = failure
			}
			err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), store, hooks)
			if stage == "validate" {
				// An inconsistent store must be refused before anything is
				// created: a rejected store leaves no half-written file behind
				// for the next run to trust.
				if err == nil || !strings.Contains(err.Error(), "is not a reusable outcome") {
					t.Fatalf("SaveMutation error = %v, want a validation refusal", err)
				}
				if creates != 0 {
					t.Fatalf("an inconsistent store reached createTemporary %d time(s)", creates)
				}
				return
			}
			if !errors.Is(err, failure) {
				t.Fatalf("SaveMutation error = %v, want %v", err, failure)
			}
		})
	}
}

// TestSaveMutationSyncsBeforeClosingExactlyOnce keeps the durability order the
// store depends on: the bytes are flushed to the device before the handle is
// given up, and the handle is given up once.
func TestSaveMutationSyncsBeforeClosingExactlyOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := &stubEvidenceFile{name: filepath.Join(root, "temporary")}
	hooks := mutationHooks{
		createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
		rename:          func(string, string) error { return nil },
		remove:          func(string) error { return nil },
	}
	if err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), mutationFixture(), hooks); err != nil {
		t.Fatal(err)
	}
	if file.writes != 1 || file.syncs != 1 || file.closes != 1 {
		t.Fatalf("writes/syncs/closes = %d/%d/%d, want 1/1/1", file.writes, file.syncs, file.closes)
	}
	// A close that fails after a successful sync pins the order: the sync
	// counter is already at one by the time Close is reached.
	closeFailure := errors.New("close failure")
	failing := &stubEvidenceFile{name: filepath.Join(root, "temporary"), closeErr: closeFailure}
	hooks.createTemporary = func(string, string) (evidenceWritableFile, error) { return failing, nil }
	if err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), mutationFixture(), hooks); !errors.Is(err, closeFailure) {
		t.Fatalf("SaveMutation error = %v, want %v", err, closeFailure)
	}
	if failing.syncs != 1 || failing.closes != 1 {
		t.Fatalf("syncs/closes = %d/%d, want 1/1", failing.syncs, failing.closes)
	}
}

func TestSaveMutationRenameFallbackDistinguishesMissingAndRemovalFailures(t *testing.T) {
	t.Parallel()
	firstRename := errors.New("first rename")
	secondRename := errors.New("second rename")
	removeFailure := errors.New("remove destination")
	for _, testCase := range []struct {
		name       string
		removeErr  error
		secondErr  error
		want       error
		wantJoined error
		wantCalls  int
	}{
		{name: "replace", wantCalls: 2},
		{name: "missing-destination", removeErr: os.ErrNotExist, secondErr: secondRename, want: secondRename, wantCalls: 2},
		{name: "remove-failure", removeErr: removeFailure, want: firstRename, wantJoined: removeFailure, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			temporary := filepath.Join(root, "temporary")
			destination := filepath.Join(root, "mutation.json")
			file := &stubEvidenceFile{name: temporary}
			renames := 0
			hooks := mutationHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
				rename: func(oldPath, newPath string) error {
					if oldPath != temporary || newPath != destination {
						t.Fatalf("rename(%q, %q)", oldPath, newPath)
					}
					renames++
					if renames == 1 {
						return firstRename
					}
					return testCase.secondErr
				},
				remove: func(path string) error {
					if path == destination {
						return testCase.removeErr
					}
					return nil
				},
			}
			err := saveMutationWithHooks(destination, mutationFixture(), hooks)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("SaveMutation error = %v", err)
				}
			} else if !errors.Is(err, testCase.want) || testCase.wantJoined != nil && !errors.Is(err, testCase.wantJoined) {
				t.Fatalf("SaveMutation error = %v, want %v joined with %v", err, testCase.want, testCase.wantJoined)
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

// TestLoadMutationReportsReadFailuresAndDecodeFailuresDistinctly keeps a
// failed read from being reported as a corrupt store: the two say different
// things about the machine the caller is on.
func TestLoadMutationReportsReadFailuresAndDecodeFailuresDistinctly(t *testing.T) {
	t.Parallel()
	failure := errors.New("read failure")
	readHooks := mutationHooks{
		readStore: func(string) ([]byte, error) {
			return []byte(`{"schema":"mutation-evidence-v1"}`), failure
		},
	}
	got, ok, err := loadMutationWithHooks("mutation.json", "example/module", readHooks)
	if !errors.Is(err, failure) || ok || !reflect.DeepEqual(got, MutationStore{}) {
		t.Fatalf("LoadMutation = %+v, ok %v, err %v", got, ok, err)
	}
	decodeHooks := mutationHooks{
		readStore: func(string) ([]byte, error) { return []byte("{"), nil },
	}
	got, ok, err = loadMutationWithHooks("mutation.json", "example/module", decodeHooks)
	if err == nil || !strings.Contains(err.Error(), "decode mutation evidence") || ok || !reflect.DeepEqual(got, MutationStore{}) {
		t.Fatalf("LoadMutation = %+v, ok %v, err %v", got, ok, err)
	}
}
