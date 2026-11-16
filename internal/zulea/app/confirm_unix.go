// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build unix

package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openConfirmation opens path without following a symlink. The returned
// FileInfo is fstat of that descriptor.
func openConfirmation(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("confirmation file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("confirmation file: %w", err)
	}
	return file, info, nil
}

// confirmDirectory requires the confirmation directory to be mode 0700 and
// owned by the current user. It is opened with O_DIRECTORY|O_NOFOLLOW and
// the check uses fstat.
func confirmDirectory(dir string) error {
	file, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("confirmation directory: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("confirmation directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("confirmation directory is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("confirmation directory mode %04o is not 0700", info.Mode().Perm())
	}
	if err := confirmOwner(info); err != nil {
		return fmt.Errorf("confirmation directory: %w", err)
	}
	return nil
}

func confirmOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("confirmation file owner cannot be read")
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("confirmation file owner %d is not the current user", stat.Uid)
	}
	return nil
}
