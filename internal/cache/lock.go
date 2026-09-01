// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const lockFileName = ".lock"

// Lease is an OS advisory exclusive lock on one cache root.
type Lease struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire waits interruptibly for exclusive ownership of root. onWait is
// called once after the first contention, before waiting for the owner.
func Acquire(ctx context.Context, root string, onWait func()) (*Lease, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("goatest: create cache lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(root, lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("goatest: open cache lock: %w", err)
	}
	waiting := false
	for {
		locked, lockErr := tryAdvisoryLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("goatest: acquire cache lock: %w", lockErr)
		}
		if locked {
			return &Lease{file: file}, nil
		}
		if !waiting {
			waiting = true
			if onWait != nil {
				onWait()
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}

// Release unlocks and closes the lease. It is safe to call more than once.
func (lease *Lease) Release() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.err = unlockAdvisory(lease.file)
		if closeErr := lease.file.Close(); lease.err == nil {
			lease.err = closeErr
		}
	})
	if lease.err != nil {
		return fmt.Errorf("goatest: release cache lock: %w", lease.err)
	}
	return nil
}
