// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !unix

package app

import (
	"errors"
	"os"
)

// openConfirmation, confirmDirectory and confirmOwner are Unix contracts
// (O_NOFOLLOW, uid, mode 0700/0600). Windows does not store those bits.
// The uploader runs on Linux.
func openConfirmation(string) (*os.File, os.FileInfo, error) {
	return nil, nil, errors.New("confirmation files require Unix permissions")
}

func confirmDirectory(string) error {
	return errors.New("confirmation directory checks require Unix permissions")
}

func confirmOwner(os.FileInfo) error { return nil }
