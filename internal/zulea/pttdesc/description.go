// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package pttdesc applies narrowly reviewed PTT-only description transforms.
package pttdesc

import "strings"

const emptyFilmwebRatingLine = "Filmweb .... : ___/10   (wpisz recznie)\n"

// StripEmptyFilmwebRating removes only the production-confirmed empty Filmweb
// rating line. Input validation, including the LF-only requirement, belongs to
// the input adapter and must run before this transform.
func StripEmptyFilmwebRating(text string) (string, bool) {
	changed := false
	anchored := "\n" + emptyFilmwebRatingLine
	for {
		before := text
		if after, ok := strings.CutPrefix(text, emptyFilmwebRatingLine); ok {
			text = after
		}
		text = strings.ReplaceAll(text, anchored, "\n")
		if text == before {
			return text, changed
		}
		changed = true
	}
}
