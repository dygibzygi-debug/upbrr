// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build unix

package freeze

import (
	"errors"
	"io/fs"
	"syscall"
)

func inodeOf(info fs.FileInfo) (uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("freeze: inode unavailable")
	}
	return uint64(st.Ino), nil //nolint:unconvert // Ino width differs between platforms.
}
