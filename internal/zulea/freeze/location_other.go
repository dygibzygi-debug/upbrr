// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !linux

package freeze

import "errors"

var errXattrUnsupported = errors.New("extended attributes are unsupported on this platform")

// unraidLocation fails closed off Linux.
func unraidLocation(string) (string, error) { return "", errXattrUnsupported }
