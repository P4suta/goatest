// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package keptledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P4suta/goatest/internal/advisorylock"
	"github.com/P4suta/goatest/internal/filemode"
)

func TestLockRetriesAfterContentionWithoutWaitingForWallTime(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".goatest", FileName)
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, filemode.PrivateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if held, err := advisorylock.Try(holder); err != nil || !held {
		t.Fatalf("holding the ledger lock = (%t, %v)", held, err)
	}
	retried := false
	release, err := lockWithWait(path, func(duration time.Duration) {
		if duration != lockPoll {
			t.Errorf("retry delay = %s, want %s", duration, lockPoll)
		}
		retried = true
		if err := advisorylock.Release(holder); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !retried {
		t.Fatal("contended lock did not retry")
	}
}
