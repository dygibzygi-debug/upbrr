// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !unix

package freeze

import (
	"errors"
	"io/fs"
)

// inodeOf is unsupported off Unix: the freeze identity requires an inode, so
// verification fails closed instead of skipping the check.
func inodeOf(fs.FileInfo) (uint64, error) {
	return 0, errors.New("freeze: inode identity is only supported on Unix")
}
