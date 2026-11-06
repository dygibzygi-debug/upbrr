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
