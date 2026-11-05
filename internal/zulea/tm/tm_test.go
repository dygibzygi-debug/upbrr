// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const rel = "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.TrueHD.Atmos.7.1-Zulea"

// enc is a minimal canonical bencode encoder for fixtures.
func enc(v any) []byte {
	var b bytes.Buffer
	switch t := v.(type) {
	case int:
		b.WriteString("i" + strconv.Itoa(t) + "e")
	case string:
		b.WriteString(strconv.Itoa(len(t)) + ":" + t)
	case []any:
		b.WriteByte('l')
		for _, item := range t {
			b.Write(enc(item))
		}
		b.WriteByte('e')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('d')
		for _, k := range keys {
			b.Write(enc(k))
			b.Write(enc(t[k]))
		}
		b.WriteByte('e')
	default:
		panic("unsupported fixture type")
	}
	return b.Bytes()
}

func filesOf(info map[string]any) []any {
	files, ok := info["files"].([]any)
	if !ok {
		panic("fixture files missing")
	}
	return files
}

func fileEntry(info map[string]any, index int) map[string]any {
	entry, ok := filesOf(info)[index].(map[string]any)
	if !ok {
		panic("fixture file entry missing")
	}
	return entry
}

func fixtureInfo() map[string]any {
	return map[string]any{
		"files": []any{
			map[string]any{"length": 30, "path": []any{rel + ".mkv"}},
			map[string]any{"length": 5, "path": []any{rel + ".nfo"}},
		},
		"name":         rel,
		"piece length": 16,
		"pieces":       strings.Repeat("A", 3*20),
		"private":      1,
	}
}

func fixture(info map[string]any) []byte {
	return enc(map[string]any{
		"announce":      "https://tracker.example.invalid/announce/PASSKEY-SENTINEL-0123456789",
		"created by":    "tm-test",
		"creation date": 1700000000,
		"info":          info,
	})
}

func expectation() Expectation {
	return Expectation{Release: rel, Sizes: map[string]int64{rel + ".mkv": 30, rel + ".nfo": 5}}
}

func TestParseAcceptsTMTorrentAndKeepsInfoBytes(t *testing.T) {
	info := fixtureInfo()
	data := fixture(info)
	got, err := Parse(data, expectation())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sum := sha1.Sum(enc(info))
	if got.InfoHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("infohash %s", got.InfoHash)
	}
	if got.Pieces != 3 || got.PieceLength != 16 || len(got.Files) != 2 {
		t.Fatalf("unexpected torrent %#v", got)
	}
	upload := got.UploadBytes()
	if bytes.Contains(upload, []byte("PASSKEY-SENTINEL")) || bytes.Contains(upload, []byte("announce")) {
		t.Fatal("upload copy must not contain the announce or passkey")
	}
	if !bytes.Contains(upload, enc(info)) {
		t.Fatal("upload copy must contain the verbatim info bytes")
	}
	hash, err := InfoHashOf(upload)
	if err != nil || hash != got.InfoHash {
		t.Fatalf("upload copy infohash %s err=%v", hash, err)
	}
	path := filepath.Join(t.TempDir(), "out", rel+".ptt.torrent")
	if err := got.WriteUploadCopy(path); err != nil {
		t.Fatalf("write upload copy: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for every writable file: os.Chmod only toggles
	// the read-only attribute, so the 0600 upload copy cannot be observed
	// there. The uploader runs on Linux, where this mode is enforced.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("upload copy mode %v", st.Mode().Perm())
	}
}

func TestParseRejectsNonConformingTorrents(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"source":       func(i map[string]any) { i["source"] = "PTT" },
		"extra key":    func(i map[string]any) { i["entropy"] = "x" },
		"no private":   func(i map[string]any) { delete(i, "private") },
		"private 0":    func(i map[string]any) { i["private"] = 0 },
		"name":         func(i map[string]any) { i["name"] = "Other.Release-Zulea" },
		"piece len":    func(i map[string]any) { i["piece length"] = 24 },
		"pieces count": func(i map[string]any) { i["pieces"] = strings.Repeat("A", 2*20) },
		"pieces width": func(i map[string]any) { i["pieces"] = strings.Repeat("A", 59) },
		"single file":  func(i map[string]any) { delete(i, "files"); i["length"] = 35 },
		"missing nfo":  func(i map[string]any) { i["files"] = []any{map[string]any{"length": 30, "path": []any{rel + ".mkv"}}} },
		"extra file": func(i map[string]any) {
			i["files"] = append(filesOf(i), map[string]any{"length": 1, "path": []any{"x.txt"}})
		},
		"size mismatch": func(i map[string]any) { fileEntry(i, 0)["length"] = 29 },
		"nested path":   func(i map[string]any) { fileEntry(i, 1)["path"] = []any{"sub", rel + ".nfo"} },
		"file extra key": func(i map[string]any) {
			fileEntry(i, 1)["md5sum"] = "x"
		},
	}
	for name, mutate := range mutations {
		info := fixtureInfo()
		mutate(info)
		if _, err := Parse(fixture(info), expectation()); !errors.Is(err, ErrTorrent) {
			t.Errorf("%s: expected ErrTorrent, got %v", name, err)
		}
	}
	raw := fixture(fixtureInfo())
	broken := map[string][]byte{
		"trailing":     append(append([]byte{}, raw...), 'x'),
		"truncated":    raw[:len(raw)-1],
		"not dict":     []byte("le"),
		"unsorted":     []byte("d4:infod4:name1:a5:filesleee"),
		"leading zero": []byte("d4:infoi01eee"),
		"empty":        nil,
	}
	for name, data := range broken {
		if _, err := Parse(data, expectation()); !errors.Is(err, ErrTorrent) {
			t.Errorf("%s: expected ErrTorrent, got %v", name, err)
		}
	}
}

func TestReadFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.torrent")
	if err := os.WriteFile(path, fixture(fixtureInfo()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err != nil {
		t.Fatalf("read: %v", err)
	}
	link := filepath.Join(dir, "b.torrent")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadFile(link); !errors.Is(err, ErrTorrent) {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing.torrent")); !errors.Is(err, ErrTorrent) {
		t.Fatalf("missing file must be a STOP: %v", err)
	}
}

func manifestText(trybe string, rows ...[]string) string {
	var b strings.Builder
	b.WriteString("# zulea-tm-batch 0.10.0\n# TRYB=" + trybe + "\n# FREEZE_SHA=" + strings.Repeat("a", 64) + "\n")
	b.WriteString(strings.Join(ManifestHeader, "\t") + "\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	b.WriteString("# WYNIK=OK\n")
	return b.String()
}

func manifestRow(release, ptt, result string) []string {
	return []string{
		release, "SRC", "100001", "11", "30", "33", "44", "5", "55", strings.Repeat("b", 64), strings.Repeat("c", 40),
		ptt, strings.Repeat("d", 64), "APPLY_OK", strings.Repeat("e", 64), result, "",
	}
}

func TestManifestProven(t *testing.T) {
	m, err := ParseManifest([]byte(manifestText("APPLY", manifestRow(rel, "APPLY_OK", "OK"))))
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.Proven(rel, "ptt")
	if err != nil || row.InfoHash != strings.Repeat("c", 40) || m.Meta["FREEZE_SHA"] == "" {
		t.Fatalf("proven row %#v err=%v", row, err)
	}
	reject := map[string]string{
		"plan mode":  manifestText("PLAN", manifestRow(rel, "DRY_RUN_OK", "OK")),
		"skipped":    manifestText("APPLY", manifestRow(rel, "APPLY_OK", "POMINIETY")),
		"ptt status": manifestText("APPLY", manifestRow(rel, "-", "OK")),
		"duplicate":  manifestText("APPLY", manifestRow(rel, "APPLY_OK", "OK"), manifestRow(rel, "ALREADY_OK", "OK")),
		"absent":     manifestText("APPLY", manifestRow("Other-Zulea", "APPLY_OK", "OK")),
	}
	for name, text := range reject {
		parsed, err := ParseManifest([]byte(text))
		if err != nil {
			t.Fatalf("%s: parse %v", name, err)
		}
		if _, err := parsed.Proven(rel, "ptt"); !errors.Is(err, ErrManifest) {
			t.Errorf("%s: expected ErrManifest, got %v", name, err)
		}
	}
	if _, err := ParseManifest([]byte("RELEASE\tX\n")); err == nil {
		t.Error("bad header must fail")
	}
}
