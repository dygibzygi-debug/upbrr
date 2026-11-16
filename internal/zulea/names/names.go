// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package names builds tracker upload titles from a Zulea release name.
//
// It is a byte-for-byte port of the Release Panel rule (names.py v0.2.2):
// the release directory, file names and torrent bytes are never changed; the
// result is only the text typed into a tracker's title field.
//
// Grammar of a Zulea release name:
//
//	<TITLE>.<YEAR>[.<PL|MULTi>][.<EXTRAS>].<RES>[.UHD].REMUX.BluRay[.DV][.HDR|.HDR10Plus|.HLG].<VIDEO>.<AUDIO>-Zulea
//
// Templates:
//
//	CM : <titles> [YEAR] [EXTRAS] [LANG] [RES] [UHD] [REMUX] [BluRay-Zulea] [DV] [HDR] [AUDIO.with.dots] [VIDEO]
//	PTT: <titles> YEAR EXTRAS LANG RES UHD BluRay REMUX DV HDR VIDEO AUDIO with spaces-Zulea
package names

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
)

// Group is the release group suffix every Zulea release name carries.
const Group = "Zulea"

// RuleVersion identifies the names.py revision this package reproduces.
const RuleVersion = "0.2.2"

var (
	resolutions = []string{"2160p", "1080p", "1080i", "720p", "576p", "576i", "480p", "480i"}
	videoCodecs = []string{"AVC", "HEVC", "VC-1", "MPEG-2"}
	hdrTokens   = []string{"DV", "HDR", "HDR10Plus", "HLG"}
	hdrFormats  = []string{"HDR", "HDR10Plus", "HLG"}
	langTokens  = []string{"PL", "MULTi"}
	audioCodecs = []string{
		"AAC", "DD", "DDPlus", "DDPlus.Atmos", "DTS", "DTS-HD.HRA", "DTS-HD.MA", "DTS-X", "FLAC",
		"LPCM", "TrueHD", "TrueHD.Atmos",
	}

	yearPattern   = regexp.MustCompile(`^(?:19|20)[0-9]{2}$`)
	layoutPattern = regexp.MustCompile(`^[0-9]\.[0-9]$`)
	discPattern   = regexp.MustCompile(`^DiSC[0-9]{1,2}$`)
	idPattern     = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// ErrName reports a release name that does not follow the Zulea grammar.
// Unknown technical tokens are rejected instead of guessed.
var ErrName = errors.New("names: invalid release name")

type nameError struct{ msg string }

func (e *nameError) Error() string { return e.msg }
func (e *nameError) Is(target error) bool {
	return target == ErrName
}

func fail(msg string) error { return &nameError{msg: msg} }

// Parsed holds the components of a Zulea release name.
type Parsed struct {
	TitleTokens []string
	Year        string
	Lang        string
	Extras      []string
	Res         string
	UHD         bool
	HDR         []string
	Video       string
	AudioCodec  string
	AudioLayout string
}

// Parse splits release into its components.
func Parse(release string) (Parsed, error) {
	suffix := "-" + Group
	if !strings.HasSuffix(release, suffix) {
		return Parsed{}, fail("nazwa nie konczy sie na " + suffix)
	}
	toks := strings.Split(strings.TrimSuffix(release, suffix), ".")
	if slices.Contains(toks, "") {
		return Parsed{}, fail("pusty element nazwy")
	}
	i := slices.Index(toks, "REMUX")
	if i < 0 {
		return Parsed{}, fail("brak REMUX")
	}
	if i+1 >= len(toks) || toks[i+1] != "BluRay" {
		return Parsed{}, fail("po REMUX oczekiwane BluRay")
	}
	left := slices.Clone(toks[:i])
	right := slices.Clone(toks[i+2:])
	uhd := len(left) > 0 && left[len(left)-1] == "UHD"
	if uhd {
		left = left[:len(left)-1]
	}
	if len(left) == 0 || !slices.Contains(resolutions, left[len(left)-1]) {
		return Parsed{}, fail("brak rozdzielczosci przed REMUX")
	}
	res := left[len(left)-1]
	left = left[:len(left)-1]
	if uhd && res != "2160p" {
		return Parsed{}, fail("UHD przy rozdzielczosci innej niz 2160p")
	}
	var mid []string
	for len(left) > 0 && !yearPattern.MatchString(left[len(left)-1]) {
		mid = append([]string{left[len(left)-1]}, mid...)
		left = left[:len(left)-1]
	}
	if len(left) < 2 {
		return Parsed{}, fail("brak tytulu albo roku")
	}
	year, titleToks := left[len(left)-1], left[:len(left)-1]
	var langs, extras []string
	for _, t := range mid {
		if slices.Contains(langTokens, t) {
			langs = append(langs, t)
		} else {
			extras = append(extras, t)
		}
	}
	if len(langs) > 1 {
		return Parsed{}, fail("wiecej niz jeden znacznik jezyka")
	}
	var hdr []string
	if len(right) > 0 && right[0] == "DV" {
		hdr = append(hdr, right[0])
		right = right[1:]
	}
	if len(right) > 0 && slices.Contains(hdrFormats, right[0]) {
		hdr = append(hdr, right[0])
		right = right[1:]
	}
	if len(right) > 0 && slices.Contains(hdrTokens, right[0]) {
		return Parsed{}, fail("niepoprawna kolejnosc albo powtorzenie DV/HDR")
	}
	if len(right) == 0 || !slices.Contains(videoCodecs, right[0]) {
		return Parsed{}, fail("nieznany kodek wideo")
	}
	video := right[0]
	right = right[1:]
	if len(right) < 3 || !layoutPattern.MatchString(strings.Join(right[len(right)-2:], ".")) {
		return Parsed{}, fail("brak ukladu kanalow audio (np. 5.1)")
	}
	codec := strings.Join(right[:len(right)-2], ".")
	layout := strings.Join(right[len(right)-2:], ".")
	if !slices.Contains(audioCodecs, codec) {
		return Parsed{}, fail("nieznany kodek audio: " + codec)
	}
	lang := ""
	if len(langs) == 1 {
		lang = langs[0]
	}
	return Parsed{
		TitleTokens: slices.Clone(titleToks),
		Year:        year,
		Lang:        lang,
		Extras:      extras,
		Res:         res,
		UHD:         uhd,
		HDR:         hdr,
		Video:       video,
		AudioCodec:  codec,
		AudioLayout: layout,
	}, nil
}

// isOtherCategory reports Unicode general category C (Cc, Cf, Co, Cs, Cn).
func isOtherCategory(r rune) bool {
	if unicode.In(r, unicode.C) {
		return true
	}
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z)
}

// clean removes category-C characters and collapses whitespace runs.
func clean(value string) string {
	var b strings.Builder
	for _, r := range value {
		if !isOtherCategory(r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isLatinLetter(r rune) bool {
	_, found := slices.BinarySearchFunc(latinLetterRanges, r, func(rng [2]rune, target rune) int {
		switch {
		case target < rng[0]:
			return 1
		case target > rng[1]:
			return -1
		default:
			return 0
		}
	})
	return found
}

// IsLatin reports whether text has letters and every letter is a LATIN letter.
func IsLatin(text string) bool {
	letters := 0
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if !isLatinLetter(r) {
			return false
		}
	}
	return letters > 0
}

var folder = cases.Fold()

func caseFoldEqual(a, b string) bool {
	return folder.String(a) == folder.String(b)
}

// TitleRecord is one row of the TMDB titles file.
type TitleRecord struct {
	TMDBID           string
	OriginalLanguage string
	TitlePL          string
	TitleOrig        string
	TitleEN          string
}

// TitlesFor returns the title text and warnings. rec may be nil.
func TitlesFor(parsed Parsed, rec *TitleRecord) (string, []string) {
	var warn []string
	fallback := strings.Join(parsed.TitleTokens, " ")
	if rec == nil {
		return fallback, append(warn, "brak tytułu z TMDB — użyto tytułu z nazwy release'u (bez polskich znaków)")
	}
	pl, orig, en := clean(rec.TitlePL), clean(rec.TitleOrig), clean(rec.TitleEN)
	lang := strings.ToLower(strings.TrimSpace(rec.OriginalLanguage))
	if lang == "pl" {
		t := pl
		if t == "" {
			t = orig
		}
		if t == "" {
			warn = append(warn, "TMDB bez tytułu — użyto tytułu z nazwy release'u")
			t = fallback
		}
		return t, warn
	}
	second := en
	if orig != "" && IsLatin(orig) {
		second = orig
	}
	if orig != "" && !IsLatin(orig) {
		warn = append(warn, "tytuł oryginalny nie jest w alfabecie łacińskim — użyto tytułu angielskiego z TMDB")
	}
	if pl == "" && second == "" {
		return fallback, append(warn, "TMDB bez tytułów — użyto tytułu z nazwy release'u")
	}
	if pl == "" {
		return second, append(warn, "brak polskiego tytułu w TMDB")
	}
	if second == "" || caseFoldEqual(pl, second) {
		return pl, warn
	}
	for _, t := range []string{pl, second} {
		if strings.Contains(t, " / ") {
			warn = append(warn, "tytuł zawiera ' / ' — sprawdź podział tytułów")
		}
	}
	return pl + " / " + second, warn
}

// Result is the outcome of Build. CinemaMovies and PTT are empty when Error is set.
type Result struct {
	CinemaMovies string
	PTT          string
	Warnings     []string
	Error        string
}

// Build returns the CinemaMovies and PTT upload titles for release.
func Build(release string, rec *TitleRecord) Result {
	p, err := Parse(release)
	if err != nil {
		return Result{Warnings: []string{}, Error: err.Error()}
	}
	title, warn := TitlesFor(p, rec)
	for _, e := range p.Extras {
		if !discPattern.MatchString(e) {
			warn = append(warn, "nietypowy znacznik w nazwie: "+e)
		}
	}
	if p.Lang == "" {
		warn = append(warn, "brak znacznika języka (PL/MULTi) w nazwie")
	}
	audioDot := p.AudioCodec + "." + p.AudioLayout
	audioSpace := strings.ReplaceAll(p.AudioCodec, ".", " ") + " " + p.AudioLayout
	cm := []string{title, "[" + p.Year + "]"}
	pt := []string{title, p.Year}
	for _, e := range p.Extras {
		cm = append(cm, "["+e+"]")
		pt = append(pt, e)
	}
	if p.Lang != "" {
		cm = append(cm, "["+p.Lang+"]")
		pt = append(pt, p.Lang)
	}
	cm = append(cm, "["+p.Res+"]")
	pt = append(pt, p.Res)
	if p.UHD {
		cm = append(cm, "[UHD]")
		pt = append(pt, "UHD")
	}
	cm = append(cm, "[REMUX]", "[BluRay-"+Group+"]")
	for _, h := range p.HDR {
		cm = append(cm, "["+h+"]")
	}
	cm = append(cm, "["+audioDot+"]", "["+p.Video+"]")
	pt = append(pt, "BluRay", "REMUX")
	pt = append(pt, p.HDR...)
	pt = append(pt, p.Video, audioSpace)
	if warn == nil {
		warn = []string{}
	}
	return Result{
		CinemaMovies: strings.Join(cm, " "),
		PTT:          strings.Join(pt, " ") + "-" + Group,
		Warnings:     warn,
	}
}

// ValidTMDBID reports whether id is a 1-10 digit TMDB identifier.
func ValidTMDBID(id string) bool { return idPattern.MatchString(id) }
