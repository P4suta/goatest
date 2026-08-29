// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package processtree starts shell-free child processes inside a platform
// primitive that permits deterministic descendant cleanup.
package processtree

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

type Tree struct {
	command *exec.Cmd
	handle  platformHandle
	once    sync.Once
	err     error
}

func Start(command *exec.Cmd) (*Tree, error) {
	prepare(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	handle, err := attach(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	return &Tree{command: command, handle: handle}, nil
}

// Kill terminates the process and every descendant in its platform group.
func (tree *Tree) Kill() error {
	if tree == nil || tree.command == nil || tree.command.Process == nil {
		return nil
	}
	err := kill(tree.command, tree.handle)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// Close releases the platform group. KILL_ON_JOB_CLOSE semantics ensure a
// provider cannot leave descendants behind after its parent exits normally.
func (tree *Tree) Close() error {
	if tree == nil {
		return nil
	}
	tree.once.Do(func() { tree.err = closeTree(tree.command, tree.handle) })
	return tree.err
}
