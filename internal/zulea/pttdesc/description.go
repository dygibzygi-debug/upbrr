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
	if strings.HasPrefix(text, emptyFilmwebRatingLine) {
		text = strings.TrimPrefix(text, emptyFilmwebRatingLine)
		changed = true
	}
	anchored := "\n" + emptyFilmwebRatingLine
	if strings.Contains(text, anchored) {
		text = strings.ReplaceAll(text, anchored, "\n")
		changed = true
	}
	return text, changed
}
