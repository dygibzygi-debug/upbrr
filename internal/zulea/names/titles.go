// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package names

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

// TitlesHeader is the exact header of tmdb-titles.tsv.
var TitlesHeader = []string{"TMDB_ID", "original_language", "title_pl", "title_orig", "title_en"}

// TitlesMaxBytes bounds the titles file size.
const TitlesMaxBytes = 8 * 1024 * 1024

// Titles maps TMDB_ID to its record.
type Titles map[string]TitleRecord

// Lookup returns the record for tmdbID, or nil when absent or malformed.
func (t Titles) Lookup(tmdbID string) *TitleRecord {
	id := strings.TrimSpace(tmdbID)
	if !ValidTMDBID(id) {
		return nil
	}
	rec, ok := t[id]
	if !ok {
		return nil
	}
	return &rec
}

// LoadTitles reads tmdb-titles.tsv. A missing file yields an empty map; a
// symbolic link, oversized file, invalid UTF-8 or malformed content is an error.
func LoadTitles(path string) (Titles, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Titles{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("names: stat titles file: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("names: titles file must not be a symbolic link")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("names: open titles file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("names: titles file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, TitlesMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("names: read titles file: %w", err)
	}
	return ParseTitles(data)
}

// ParseTitles parses the bytes of tmdb-titles.tsv.
func ParseTitles(data []byte) (Titles, error) {
	if len(data) > TitlesMaxBytes {
		return nil, errors.New("plik tytulow za duzy")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("plik tytulow nie jest poprawnym UTF-8")
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || !slices.Equal(strings.Split(strings.TrimRight(lines[0], "\r"), "\t"), TitlesHeader) {
		return nil, errors.New("naglowek pliku tytulow inny niz oczekiwany")
	}
	out := make(Titles, len(lines)-1)
	for _, raw := range lines[1:] {
		f := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(f) != len(TitlesHeader) || !ValidTMDBID(f[0]) {
			return nil, errors.New("niepoprawny wiersz pliku tytulow")
		}
		if _, dup := out[f[0]]; dup {
			return nil, fmt.Errorf("powtorzony TMDB_ID %s w pliku tytulow", f[0])
		}
		out[f[0]] = TitleRecord{
			TMDBID:           f[0],
			OriginalLanguage: f[1],
			TitlePL:          f[2],
			TitleOrig:        f[3],
			TitleEN:          f[4],
		}
	}
	return out, nil
}
