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

func TestStripEmptyFilmwebRatingRequiresLineStart(t *testing.T) {
	const input = "prefix Filmweb .... : ___/10   (wpisz recznie)\nIMDb ....... : 6.4/10\n"
	got, changed := StripEmptyFilmwebRating(input)
	if changed || got != input {
		t.Fatalf("same-line prefix changed=%t got=%q", changed, got)
	}
}

func TestStripEmptyFilmwebRatingAtTextStart(t *testing.T) {
	const input = "Filmweb .... : ___/10   (wpisz recznie)\nIMDb ....... : 6.4/10\n"
	const want = "IMDb ....... : 6.4/10\n"
	got, changed := StripEmptyFilmwebRating(input)
	if !changed || got != want {
		t.Fatalf("first line changed=%t got=%q want=%q", changed, got, want)
	}
}

func TestStripEmptyFilmwebRatingRemovesConsecutiveLines(t *testing.T) {
	const line = "Filmweb .... : ___/10   (wpisz recznie)\n"
	tests := map[string]string{
		"at text start": line + line + "IMDb ....... : 6.4/10\n",
		"inside block":  "[pre]\n" + line + line + "IMDb ....... : 6.4/10\n[/pre]\n",
	}
	wants := map[string]string{
		"at text start": "IMDb ....... : 6.4/10\n",
		"inside block":  "[pre]\nIMDb ....... : 6.4/10\n[/pre]\n",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			got, changed := StripEmptyFilmwebRating(input)
			if !changed || got != wants[name] {
				t.Fatalf("changed=%t got=%q want=%q", changed, got, wants[name])
			}
			again, changedAgain := StripEmptyFilmwebRating(got)
			if changedAgain || again != got {
				t.Fatalf("not idempotent: changed=%t got=%q", changedAgain, again)
			}
		})
	}
}
