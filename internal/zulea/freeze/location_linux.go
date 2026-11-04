// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package freeze

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// unraidLocation returns the system.LOCATION attribute Unraid's shfs exposes
// on user-share files (the array disk name, e.g. "disk1").
func unraidLocation(path string) (string, error) {
	buf := make([]byte, 256)
	n, err := unix.Getxattr(path, "system.LOCATION", buf)
	if err != nil {
		return "", fmt.Errorf("getxattr system.LOCATION: %w", err)
	}
	return strings.TrimRight(strings.TrimSpace(string(buf[:n])), "\x00"), nil
}
