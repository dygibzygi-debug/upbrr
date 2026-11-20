// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package clilock serializes stateful and networked zulea-upbrr commands.
package clilock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const fileName = ".zulea-upbrr.lock"

// ErrBusy means another zulea-upbrr command owns the global CLI lock.
var ErrBusy = errors.New("busy")

// Lock is held until Close or process exit.
type Lock struct {
	file *os.File
}

// Acquire opens and non-blockingly locks work/.zulea-upbrr.lock. The work
// directory must already exist; callers must not silently create a new state
// tree because of a typo in the configured path.
func Acquire(workDir string) (*Lock, error) {
	if workDir == "" {
		return nil, errors.New("cli lock: empty work directory")
	}
	file, err := open(filepath.Join(workDir, fileName))
	if err != nil {
		return nil, fmt.Errorf("cli lock: %w", err)
	}
	if err := validate(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("cli lock: %w", err)
	}
	if err := tryLock(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrBusy) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("cli lock: %w", err)
	}
	return &Lock{file: file}, nil
}

// Close releases the lock.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("close cli lock: %w", err)
	}
	return nil
}

func validate(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("fstat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("lock path is not a regular file")
	}
	if err := validateOwner(info); err != nil {
		return err
	}
	if enforceMode() && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("lock file mode is %04o, expected 0600", info.Mode().Perm())
	}
	return nil
}
