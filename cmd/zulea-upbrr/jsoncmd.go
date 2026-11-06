// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"

	"github.com/autobrr/upbrr/internal/zulea/app"
)

func commandPreview(asJSON bool, stdout io.Writer) io.Writer {
	if asJSON {
		return io.Discard
	}
	return stdout
}

func writeCommandJSON(stdout, stderr io.Writer, command string, code int, result any, err error, logger *app.Logger) int {
	var mask func(string) string
	if logger != nil {
		mask = logger.MaskKnown
		if err != nil {
			err = fmt.Errorf("%s", logger.Sanitize(err.Error()))
		}
	}
	if werr := app.WritePublicJSON(stdout, command, code, result, err, mask); werr != nil {
		fmt.Fprintf(stderr, "zulea-upbrr %s: %v\n", command, werr)
		return exitRuntime
	}
	return code
}
