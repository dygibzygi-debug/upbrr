// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !unix

package app

import "os"

// confirmOwner is a no-op outside Unix. Windows has no Unix uid to compare.
// The mode 0600 check in ConsumeConfirmation still runs. The uploader itself
// runs on Linux, where confirm_unix.go enforces the owner.
func confirmOwner(os.FileInfo) error { return nil }
