// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomicPrivate replaces path with data at mode 0600. The temporary file
// is synced and renamed in the same directory, so a crash leaves the previous
// file or the new one.
func writeAtomicPrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("private file: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("private file: %w", err)
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("private file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("private file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("private file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("private file: %w", err)
	}
	syncDir(dir)
	return nil
}
