// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package freeze

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/autobrr/upbrr/internal/pathing"
)

// Disk check modes (W-2). One must be chosen explicitly.
const (
	// DiskModePath requires the MKV to be read from <MountBase>/<disk>/..., the
	// array-disk path the Torrent Maker wrapper also uses (SOURCE_ROOT).
	DiskModePath = "path"
	// DiskModeUnraidXattr reads the Unraid user-share attribute system.LOCATION
	// of a /mnt/user path, which names the array disk holding the file.
	DiskModeUnraidXattr = "unraid_xattr"
)

// DefaultMountBase is the Unraid array mount root.
const DefaultMountBase = "/mnt"

var diskPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// DiskCheck verifies the freeze column physical_N5_disk against the real file
// location, like the TM wrapper's PHYS_DISK check.
type DiskCheck struct {
	// Disk is the expected array disk, e.g. "disk1".
	Disk string
	// Mode is DiskModePath or DiskModeUnraidXattr.
	Mode string
	// MountBase overrides DefaultMountBase (tests).
	MountBase string
}

// Validate checks the configuration.
func (c DiskCheck) Validate() error {
	if !diskPattern.MatchString(c.Disk) {
		return fmt.Errorf("freeze: physical disk %q is invalid", c.Disk)
	}
	switch c.Mode {
	case DiskModePath, DiskModeUnraidXattr:
		return nil
	default:
		return fmt.Errorf("freeze: disk check mode %q is unknown (use %q or %q)", c.Mode, DiskModePath, DiskModeUnraidXattr)
	}
}

// Verify stops the release unless the freeze record and the file location
// both name the configured disk.
func (c DiskCheck) Verify(rec Record, mkvPath string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if rec.Disk != c.Disk {
		return fmt.Errorf("%w: physical_N5_disk=%s (required %s)", ErrNotEligible, rec.Disk, c.Disk)
	}
	switch c.Mode {
	case DiskModePath:
		base := c.MountBase
		if base == "" {
			base = DefaultMountBase
		}
		if !pathing.IsWithinRoot(filepath.Join(base, c.Disk), mkvPath) {
			return fmt.Errorf("%w: MKV is not read from the %s array path", ErrNotEligible, c.Disk)
		}
		return nil
	default:
		location, err := unraidLocation(mkvPath)
		if err != nil {
			return fmt.Errorf("%w: cannot read Unraid file location: %w", ErrNotEligible, err)
		}
		disks := strings.FieldsFunc(location, func(r rune) bool { return r == ',' || r == ' ' })
		if len(disks) != 1 || disks[0] != c.Disk {
			return fmt.Errorf("%w: Unraid location %q differs from %s", ErrNotEligible, location, c.Disk)
		}
		return nil
	}
}
