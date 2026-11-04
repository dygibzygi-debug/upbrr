// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package names

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type goldenFile struct {
	RuleVersion string         `json:"rule_version"`
	Results     []goldenResult `json:"results"`
}

type goldenResult struct {
	Release      string   `json:"release"`
	Record       *string  `json:"record"`
	CinemaMovies *string  `json:"cinemamovies"`
	PTT          *string  `json:"ptt"`
	Warnings     []string `json:"warnings"`
	Error        *string  `json:"error"`
}

type caseFile struct {
	Records map[string]map[string]string `json:"records"`
}

func loadJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func recordFrom(fields map[string]string) *TitleRecord {
	if fields == nil {
		return nil
	}
	return &TitleRecord{
		TMDBID:           fields["TMDB_ID"],
		OriginalLanguage: fields["original_language"],
		TitlePL:          fields["title_pl"],
		TitleOrig:        fields["title_orig"],
		TitleEN:          fields["title_en"],
	}
}

func assertGolden(t *testing.T, golden goldenFile, records map[string]map[string]string) {
	t.Helper()
	if golden.RuleVersion != RuleVersion {
		t.Fatalf("golden rule version %q, package %q", golden.RuleVersion, RuleVersion)
	}
	for _, want := range golden.Results {
		var rec *TitleRecord
		if want.Record != nil {
			fields, ok := records[*want.Record]
			if !ok {
				t.Fatalf("unknown record key %q", *want.Record)
			}
			rec = recordFrom(fields)
		}
		got := Build(want.Release, rec)
		label := want.Release
		if want.Record != nil {
			label += " [" + *want.Record + "]"
		}
		if want.Error != nil {
			if got.Error != *want.Error || got.CinemaMovies != "" || got.PTT != "" {
				t.Errorf("%s: error got %q want %q", label, got.Error, *want.Error)
			}
			continue
		}
		if got.Error != "" {
			t.Errorf("%s: unexpected error %q", label, got.Error)
			continue
		}
		if want.CinemaMovies == nil || got.CinemaMovies != *want.CinemaMovies {
			t.Errorf("%s: cinemamovies got %q want %v", label, got.CinemaMovies, deref(want.CinemaMovies))
		}
		if want.PTT == nil || got.PTT != *want.PTT {
			t.Errorf("%s: ptt got %q want %v", label, got.PTT, deref(want.PTT))
		}
		if !slices.Equal(got.Warnings, want.Warnings) {
			t.Errorf("%s: warnings got %q want %q", label, got.Warnings, want.Warnings)
		}
	}
}

func deref(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

// TestBuildMatchesReferenceGolden compares every synthetic case with the
// output of the reference names.py (scripts/zulea/gen_names_golden.py).
func TestBuildMatchesReferenceGolden(t *testing.T) {
	var cases caseFile
	loadJSON(t, filepath.Join("testdata", "cases.json"), &cases)
	var golden goldenFile
	loadJSON(t, filepath.Join("testdata", "golden.json"), &golden)
	if len(golden.Results) == 0 {
		t.Fatal("golden file has no results")
	}
	assertGolden(t, golden, cases.Records)
}

// TestBuildMatchesPrivateGolden runs the same comparison on a private sample
// that never enters the repository. Set ZULEA_NAMES_PRIVATE_CASES and
// ZULEA_NAMES_PRIVATE_GOLDEN to the cases and golden JSON produced locally.
func TestBuildMatchesPrivateGolden(t *testing.T) {
	casesPath := os.Getenv("ZULEA_NAMES_PRIVATE_CASES")
	goldenPath := os.Getenv("ZULEA_NAMES_PRIVATE_GOLDEN")
	if casesPath == "" || goldenPath == "" {
		t.Skip("private name sample not configured")
	}
	var cases caseFile
	loadJSON(t, casesPath, &cases)
	var golden goldenFile
	loadJSON(t, goldenPath, &golden)
	assertGolden(t, golden, cases.Records)
	t.Logf("private sample: %d names compared", len(golden.Results))
}

func TestParseErrorsWrapErrName(t *testing.T) {
	_, err := Parse("Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.TrueHD.Atmos.7.1-GRP")
	if !errors.Is(err, ErrName) {
		t.Fatalf("expected ErrName, got %v", err)
	}
}

func TestParseTitles(t *testing.T) {
	header := strings.Join(TitlesHeader, "\t")
	valid := header + "\r\n100001\ten\tPrzykład\tExample\tExample\n"
	titles, err := ParseTitles([]byte(valid))
	if err != nil {
		t.Fatalf("parse valid titles: %v", err)
	}
	rec := titles.Lookup(" 100001 ")
	if rec == nil || rec.TitlePL != "Przykład" || rec.TitleEN != "Example" {
		t.Fatalf("lookup got %#v", rec)
	}
	if titles.Lookup("abc") != nil || titles.Lookup("999") != nil {
		t.Fatal("lookup must reject malformed and unknown IDs")
	}
	rejected := map[string]string{
		"header":    "TMDB_ID\ttitle\n",
		"empty":     "",
		"columns":   header + "\n100001\ten\tA\tB\n",
		"id":        header + "\nx1\ten\tA\tB\tC\n",
		"blankline": header + "\n\n100001\ten\tA\tB\tC\n",
		"duplicate": header + "\n100001\ten\tA\tB\tC\n100001\ten\tA\tB\tC\n",
		"utf8":      header + "\n100001\ten\t\xff\tB\tC\n",
	}
	for name, body := range rejected {
		if _, err := ParseTitles([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := ParseTitles(make([]byte, TitlesMaxBytes+1)); err == nil {
		t.Error("oversized titles must be rejected")
	}
}

func TestLoadTitlesMissingAndSymlink(t *testing.T) {
	dir := t.TempDir()
	titles, err := LoadTitles(filepath.Join(dir, "missing.tsv"))
	if err != nil || len(titles) != 0 {
		t.Fatalf("missing file: titles=%v err=%v", titles, err)
	}
	realPath := filepath.Join(dir, "titles.tsv")
	if err := os.WriteFile(realPath, []byte(strings.Join(TitlesHeader, "\t")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTitles(realPath); err != nil {
		t.Fatalf("load real file: %v", err)
	}
	link := filepath.Join(dir, "link.tsv")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadTitles(link); err == nil {
		t.Fatal("symbolic link must be rejected")
	}
}

func TestLatinTableVersion(t *testing.T) {
	if latinNameUnicodeVersion == "" || len(latinLetterRanges) == 0 {
		t.Fatal("latin letter table is empty")
	}
}
