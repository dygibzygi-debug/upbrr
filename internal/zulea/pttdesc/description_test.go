// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttdesc

import "testing"

func TestStripEmptyFilmwebRating(t *testing.T) {
	const before = "[b]OCENY[/b]\n[pre]\n" +
		"Filmweb .... : ___/10   (wpisz recznie)\n" +
		"IMDb ....... : 6.4/10 (252565 glosow)\n" +
		"TMDB ....... : 6.8/10 (5658 glosow)\n[/pre]\n"
	const want = "[b]OCENY[/b]\n[pre]\n" +
		"IMDb ....... : 6.4/10 (252565 glosow)\n" +
		"TMDB ....... : 6.8/10 (5658 glosow)\n[/pre]\n"

	got, changed := StripEmptyFilmwebRating(before)
	if !changed || got != want {
		t.Fatalf("changed=%t\ngot:  %q\nwant: %q", changed, got, want)
	}
	again, changedAgain := StripEmptyFilmwebRating(got)
	if changedAgain || again != got {
		t.Fatalf("second application changed=%t got=%q want=%q", changedAgain, again, got)
	}
}

func TestStripEmptyFilmwebRatingLeavesOtherTextByteExact(t *testing.T) {
	tests := map[string]string{
		"no placeholder":         "[pre]\nIMDb ....... : 6.4/10\n[/pre]\n",
		"numeric Filmweb rating": "[pre]\nFilmweb .... : 7.1/10\nIMDb ....... : 6.4/10\n[/pre]\n",
		"different spacing":      "[pre]\nFilmweb .... : ___/10  (wpisz recznie)\n[/pre]\n",
		"link":                   "[url=https://filmweb.example.invalid/title]Filmweb[/url]\n",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			got, changed := StripEmptyFilmwebRating(input)
			if changed || got != input {
				t.Fatalf("changed=%t got=%q want byte-exact %q", changed, got, input)
			}
		})
	}
}

func TestStripEmptyFilmwebRatingRequiresTrailingLF(t *testing.T) {
	const input = "Filmweb .... : ___/10   (wpisz recznie)"
	got, changed := StripEmptyFilmwebRating(input)
	if changed || got != input {
		t.Fatalf("unterminated line changed=%t got=%q", changed, got)
	}
}
