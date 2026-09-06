// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package filemode

import "io/fs"

const (
	AnyExecute fs.FileMode = 0o111

	OwnerWrite fs.FileMode = 0o200

	PrivateFile fs.FileMode = 0o600

	GroupReadableFile fs.FileMode = 0o640

	ReadableFile fs.FileMode = 0o644

	PrivateDirectory fs.FileMode = 0o700

	GroupReadableDirectory fs.FileMode = 0o750

	ReadableDirectory fs.FileMode = 0o755
)
