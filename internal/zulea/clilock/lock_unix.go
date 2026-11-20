//go:build !windows

// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package clilock

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func open(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open without following symlinks: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("lock owner is unavailable")
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return fmt.Errorf("lock file owner is uid %d, expected %d", stat.Uid, os.Geteuid())
	}
	return nil
}

func enforceMode() bool { return true }

func tryLock(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrBusy
		}
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}
