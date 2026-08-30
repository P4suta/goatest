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

var (
	prepareTreeCommand = prepare
	startTreeCommand   = func(command *exec.Cmd) error { return command.Start() }
	attachTreeCommand  = attach
	killStartedCommand = func(command *exec.Cmd) error { return command.Process.Kill() }
	waitStartedCommand = func(command *exec.Cmd) error { return command.Wait() }
	killTreeCommand    = kill
	closeTreeCommand   = closeTree
)

func Start(command *exec.Cmd) (*Tree, error) {
	if command == nil {
		return nil, errors.New("goatest: nil process-tree command")
	}
	prepareTreeCommand(command)
	if err := startTreeCommand(command); err != nil {
		return nil, err
	}
	handle, err := attachTreeCommand(command)
	if err != nil {
		_ = killStartedCommand(command)
		_ = waitStartedCommand(command)
		return nil, err
	}
	return &Tree{command: command, handle: handle}, nil
}

// Kill terminates the process and every descendant in its platform group.
func (tree *Tree) Kill() error {
	if tree == nil || tree.command == nil || tree.command.Process == nil {
		return nil
	}
	err := killTreeCommand(tree.command, tree.handle)
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
	tree.once.Do(func() { tree.err = closeTreeCommand(tree.command, tree.handle) })
	return tree.err
}
