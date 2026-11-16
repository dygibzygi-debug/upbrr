// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"crypto/sha1" //nolint:gosec // BitTorrent v1 infohash is defined as SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Description summarizes one torrent without exposing announce URLs: tracker
// addresses keep only scheme and host plus whether a path or query (where a
// passkey usually lives) is present.
type Description struct {
	SHA256       string
	InfoHash     string
	Name         string
	PieceLength  int64
	PiecesSHA256 string
	Private      int64
	PrivateSet   bool
	Source       string
	SourceSet    bool
	TopKeys      []string
	InfoKeys     []string
	Files        []File
	Trackers     []string
}

// Describe decodes torrent bytes leniently (tracker re-encodings included).
func Describe(data []byte) (Description, error) {
	root, err := decodeLenient(data)
	if err != nil {
		return Description{}, fmt.Errorf("tm: %w", err)
	}
	top, ok := root.(*dict)
	if !ok {
		return Description{}, errors.New("tm: torrent root is not a dictionary")
	}
	info, ok := top.values["info"].(*dict)
	if !ok {
		return Description{}, errors.New("tm: torrent has no info dictionary")
	}
	d := Description{TopKeys: sortedKeys(top), InfoKeys: sortedKeys(info)}
	digest := sha256.Sum256(data)
	d.SHA256 = hex.EncodeToString(digest[:])
	hash := sha1.Sum(top.raw["info"]) //nolint:gosec // BitTorrent v1 infohash.
	d.InfoHash = hex.EncodeToString(hash[:])
	if name, ok := info.values["name"].([]byte); ok {
		d.Name = string(name)
	}
	d.PieceLength, _ = info.values["piece length"].(int64)
	if pieces, ok := info.values["pieces"].([]byte); ok {
		sum := sha256.Sum256(pieces)
		d.PiecesSHA256 = hex.EncodeToString(sum[:])
	}
	d.Private, d.PrivateSet = info.values["private"].(int64)
	if source, ok := info.values["source"].([]byte); ok {
		d.Source, d.SourceSet = string(source), true
	}
	if files, ok := info.values["files"].([]value); ok {
		for _, item := range files {
			entry, ok := item.(*dict)
			if !ok {
				return Description{}, errors.New("tm: malformed files entry")
			}
			length, _ := entry.values["length"].(int64)
			parts, _ := entry.values["path"].([]value)
			names := make([]string, 0, len(parts))
			for _, part := range parts {
				if b, ok := part.([]byte); ok {
					names = append(names, string(b))
				}
			}
			d.Files = append(d.Files, File{Path: strings.Join(names, "/"), Length: length})
		}
	} else if length, ok := info.values["length"].(int64); ok {
		d.Files = []File{{Path: d.Name, Length: length}}
	}
	var trackers []string
	if announce, ok := top.values["announce"].([]byte); ok {
		trackers = append(trackers, maskTracker(string(announce)))
	}
	if tiers, ok := top.values["announce-list"].([]value); ok {
		for _, tier := range tiers {
			list, _ := tier.([]value)
			for _, item := range list {
				if b, ok := item.([]byte); ok {
					trackers = append(trackers, maskTracker(string(b)))
				}
			}
		}
	}
	sort.Strings(trackers)
	d.Trackers = slices.Compact(trackers)
	return d, nil
}

func sortedKeys(d *dict) []string {
	keys := slices.Clone(d.keys)
	sort.Strings(keys)
	return keys
}

func maskTracker(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<nieczytelny>"
	}
	extra := (u.Path != "" && u.Path != "/" && u.Path != "/announce" && u.Path != "/announce.php") || u.RawQuery != ""
	flag := "NIE"
	if extra {
		flag = "TAK"
	}
	return u.Scheme + "://" + u.Hostname() + "  (dodatkowy element w adresie: " + flag + ")"
}

// Comparison is the verdict of comparing the TM torrent with the torrent
// downloaded from the tracker (port of porownaj-torrent-z-trackera.py).
type Comparison struct {
	Original   Description
	Downloaded Description
	// DataMatch: same pieces, piece length and file lengths (seeding is safe).
	DataMatch bool
	// NamesMatch: same info.name and file paths.
	NamesMatch bool
	// InfoHashChanged: the tracker changed the info dictionary (e.g. added source).
	InfoHashChanged bool
	// PrivateOK: the downloaded torrent has private=1.
	PrivateOK bool
}

// OK reports DANE_ZGODNE=TAK and PRIVATE=1, the condition for adding the
// downloaded torrent to the client (with a full recheck).
func (c Comparison) OK() bool { return c.DataMatch && c.PrivateOK }

// Compare compares the TM original with the downloaded torrent.
func Compare(original, downloaded []byte) (Comparison, error) {
	a, err := Describe(original)
	if err != nil {
		return Comparison{}, fmt.Errorf("original: %w", err)
	}
	b, err := Describe(downloaded)
	if err != nil {
		return Comparison{}, fmt.Errorf("downloaded: %w", err)
	}
	lengths := func(files []File) []int64 {
		out := make([]int64, 0, len(files))
		for _, f := range files {
			out = append(out, f.Length)
		}
		return out
	}
	paths := func(files []File) []string {
		out := make([]string, 0, len(files))
		for _, f := range files {
			out = append(out, f.Path)
		}
		return out
	}
	return Comparison{
		Original:   a,
		Downloaded: b,
		DataMatch: a.PiecesSHA256 != "" && a.PiecesSHA256 == b.PiecesSHA256 && a.PieceLength == b.PieceLength &&
			slices.Equal(lengths(a.Files), lengths(b.Files)),
		NamesMatch:      a.Name == b.Name && slices.Equal(paths(a.Files), paths(b.Files)),
		InfoHashChanged: a.InfoHash != b.InfoHash,
		PrivateOK:       b.PrivateSet && b.Private == 1,
	}, nil
}

// Report renders the comparison in the format of the reference script.
func (c Comparison) Report() string {
	var w strings.Builder
	a, b := c.Original, c.Downloaded
	row := func(key string, va, vb string) {
		same := "="
		if va != vb {
			same = "ROZNE"
		}
		fmt.Fprintf(&w, "%-13s %-6s oryginal: %s\n", key, same, va)
		if va != vb {
			fmt.Fprintf(&w, "%-13s %-6s pobrany:  %s\n", "", "", vb)
		}
	}
	opt := func(set bool, v string) string {
		if !set {
			return "None"
		}
		return v
	}
	w.WriteString("== ORYGINAL (TM) vs POBRANY (tracker) ==\n")
	row("sha256", a.SHA256, b.SHA256)
	row("infohash", a.InfoHash, b.InfoHash)
	row("name", a.Name, b.Name)
	row("piece_length", strconv.FormatInt(a.PieceLength, 10), strconv.FormatInt(b.PieceLength, 10))
	row("private", opt(a.PrivateSet, strconv.FormatInt(a.Private, 10)), opt(b.PrivateSet, strconv.FormatInt(b.Private, 10)))
	row("source", opt(a.SourceSet, a.Source), opt(b.SourceSet, b.Source))
	row("top_keys", fmt.Sprint(a.TopKeys), fmt.Sprint(b.TopKeys))
	row("info_keys", fmt.Sprint(a.InfoKeys), fmt.Sprint(b.InfoKeys))
	pieces := "="
	if a.PiecesSHA256 != b.PiecesSHA256 {
		pieces = "ROZNE"
	}
	fmt.Fprintf(&w, "%-13s %-6s (SHA-256 tablicy SHA-1 kawalkow)\n", "pieces", pieces)
	files := "="
	if !slices.Equal(a.Files, b.Files) {
		files = "ROZNE"
	}
	fmt.Fprintf(&w, "%-13s %-6s %d vs %d\n", "pliki", files, len(a.Files), len(b.Files))
	for i := 0; i < len(a.Files) && i < len(b.Files); i++ {
		fmt.Fprintf(&w, "%-20s %15d %s\n", "", a.Files[i].Length, a.Files[i].Path)
		if a.Files[i] != b.Files[i] {
			fmt.Fprintf(&w, "%-20s %15d %s  <- pobrany\n", "", b.Files[i].Length, b.Files[i].Path)
		}
	}
	w.WriteString("trackery (bez passkeya):\n")
	for _, t := range b.Trackers {
		fmt.Fprintf(&w, "%-20s %s\n", "", t)
	}
	yes := func(v bool) string {
		if v {
			return "TAK"
		}
		return "NIE"
	}
	w.WriteString("\nWNIOSEK:\n")
	fmt.Fprintf(&w, "  DANE_ZGODNE=%s   (kawalki, rozmiar kawalka i dlugosci plikow)\n", yes(c.DataMatch))
	if c.NamesMatch {
		w.WriteString("  NAZWY_ZGODNE=TAK\n")
	} else {
		w.WriteString("  NAZWY_ZGODNE=NIE — przy dodawaniu do klienta ustaw nazwe katalogu = nazwa release\n")
	}
	fmt.Fprintf(&w, "  INFOHASH_ZMIENIONY_PRZEZ_TRACKER=%s\n", yes(c.InfoHashChanged))
	if c.PrivateOK {
		w.WriteString("  PRIVATE=1\n")
	} else {
		w.WriteString("  PRIVATE=BRAK/0 — STOP\n")
	}
	return w.String()
}
