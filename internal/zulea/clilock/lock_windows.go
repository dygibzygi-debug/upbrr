//go:build windows

// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package clilock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func open(path string) (*os.File, error) {
	info, err := os.Lstat(path) //nolint:gosec // Path is the configured work_dir plus a fixed lock file name.
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("lock path is a symlink")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("lstat: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Path is the configured work_dir plus a fixed lock file name.
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return file, nil
}

func validateOwner(os.FileInfo) error { return nil }

// Windows exposes only a read-only bit through os.FileMode; exact POSIX mode
// enforcement is performed on the Linux production platform.
func enforceMode() bool { return false }

func tryLock(file *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrBusy
		}
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}
