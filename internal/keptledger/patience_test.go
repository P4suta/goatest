// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package keptledger_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/keptledger"
)

// TestAppendWaitsOutAWriterThatHoldsTheLockForSeconds is the patience a ledger
// write needs on a loaded machine. The dogfood's race phase — every package's
// tests under the race detector, eight at a time — held the ledger lock across
// two goroutines for longer than two seconds between one writer's turns, and a
// wait that gives up that early turns a slow neighbour into a lost record.
// Whoever holds the lock is another goatest writing the same file for a few
// milliseconds; waiting for it is always right, and the bound exists only so
// that a dead holder cannot hang a run forever.
func TestAppendWaitsOutAWriterThatHoldsTheLockForSeconds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".goatest", keptledger.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if held, err := advisorylock.Try(holder); err != nil || !held {
		t.Fatalf("holding the ledger lock = (%t, %v)", held, err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(3 * time.Second)
		_ = advisorylock.Release(holder)
		close(released)
	}()
	if err := keptledger.Append(path, keptledger.Entry{Path: "/kept", RunID: "run", KeptAt: time.Now()}); err != nil {
		t.Fatalf("append while another writer held the lock for three seconds = %v, want it to wait", err)
	}
	<-released
	ledger, err := keptledger.Load(path)
	if err != nil || len(ledger.Entries) != 1 {
		t.Fatalf("ledger after the wait = (%+v, %v), want the one entry", ledger, err)
	}
}
