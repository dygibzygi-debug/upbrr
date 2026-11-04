// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package zuleatest builds a complete synthetic Zulea environment (library,
// freeze, titles, TM manifest and torrent) for offline tests. Every name and
// value is synthetic; the announce carries a sentinel passkey that tests assert
// never leaks.
package zuleatest

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // BitTorrent v1 infohash fixture.
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
)

// Release is the synthetic release used by the fixture.
const Release = "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.TrueHD.Atmos.7.1-Zulea"

// Disk is the synthetic array disk recorded in the freeze.
const Disk = "disk1"

// TMDBID is the synthetic TMDB identifier recorded in the freeze.
const TMDBID = "100001"

// PasskeySentinel is embedded in the TM announce and screens.json; it must never leak.
const PasskeySentinel = "PASSKEYSENTINEL0123456789abcdef"

// DeleteSentinel is the screens.json delete URL token; it must never be read.
const DeleteSentinel = "DELETESENTINEL9876543210"

// Description is the synthetic ready description.
const Description = "[center][b]Example Film[/b][/center]\n\n[b]SCREENY[/b]\n" +
	"[url=https://ibb.co/aaaa][img]https://i.ibb.co/aaaa/shot1.png[/img][/url]\n" +
	"[url=https://ibb.co/bbbb][img]https://i.ibb.co/bbbb/shot2.png[/img][/url]\n" +
	"[url=https://ibb.co/cccc][img]https://i.ibb.co/cccc/shot3.png[/img][/url]\n"

// Env is a generated environment.
type Env struct {
	Root         string
	MountBase    string
	LibraryRoot  string
	ReleaseDir   string
	MKVPath      string
	NFOPath      string
	ScreensPath  string
	FreezePath   string
	FreezeSHA256 string
	TitlesPath   string
	ManifestPath string
	TorrentDir   string
	TorrentPath  string
	InfoHash     string
	NFOBytes     []byte
	MKVBytes     []byte
}

// Options tweak the fixture.
type Options struct {
	// ManifestPTTStatus overrides PTT_STATUS (default APPLY_OK).
	ManifestPTTStatus string
	// TorrentInfoExtra adds keys to the TM info dictionary (e.g. "source").
	TorrentInfoExtra map[string]any
}

// Encode is a canonical bencode encoder for fixtures (int, string, []any, map[string]any).
func Encode(v any) []byte {
	var b bytes.Buffer
	switch t := v.(type) {
	case int:
		b.WriteString("i" + strconv.Itoa(t) + "e")
	case int64:
		b.WriteString("i" + strconv.FormatInt(t, 10) + "e")
	case string:
		b.WriteString(strconv.Itoa(len(t)) + ":" + t)
	case []any:
		b.WriteByte('l')
		for _, item := range t {
			b.Write(Encode(item))
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
			b.Write(Encode(k))
			b.Write(Encode(t[k]))
		}
		b.WriteByte('e')
	default:
		panic("zuleatest: unsupported bencode type")
	}
	return b.Bytes()
}

func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func statTriple(t *testing.T, path string) (string, string, string) {
	t.Helper()
	id, err := freeze.StatIdentity(path)
	if err != nil {
		t.Skipf("inode identity unavailable: %v", err)
	}
	return strconv.FormatUint(id.Ino, 10), strconv.FormatInt(id.Size, 10), strconv.FormatInt(id.Mtime, 10)
}

// NFO returns a synthetic CP437-style NFO whose RELEASE field names release.
func NFO(release string) []byte {
	return []byte("\xc9\xcd\xcd\xbb\r\n  RELEASE.........: " + release + "\r\n  SOURCE..........: Blu-ray\r\n\xc8\xcd\xcd\xbc\r\n")
}

// New builds the environment under t.TempDir().
func New(t *testing.T, opts Options) *Env {
	t.Helper()
	root := t.TempDir()
	env := &Env{
		Root:        root,
		MountBase:   filepath.Join(root, "mnt"),
		LibraryRoot: filepath.Join(root, "mnt", Disk, "library"),
		TorrentDir:  filepath.Join(root, "tm-output", "run-1"),
	}
	env.ReleaseDir = filepath.Join(env.LibraryRoot, Release)
	env.MKVPath = filepath.Join(env.ReleaseDir, Release+".mkv")
	env.NFOPath = filepath.Join(env.ReleaseDir, Release+".nfo")
	env.ScreensPath = filepath.Join(env.ReleaseDir, Release+".screens.json")
	env.MKVBytes = bytes.Repeat([]byte("M"), 40)
	env.NFOBytes = NFO(Release)
	write(t, env.MKVPath, env.MKVBytes, 0o644)
	write(t, env.NFOPath, env.NFOBytes, 0o644)
	write(t, filepath.Join(env.ReleaseDir, Release+".source.txt"), []byte("SOURCE_ID=Synthetic.Source\n"), 0o644)
	write(t, filepath.Join(env.ReleaseDir, Release+".cinemamovies.txt"), []byte(Description), 0o644)
	write(t, env.ScreensPath, []byte(`{"shots":[{"delete":"https://ibb.co/`+DeleteSentinel+`","key":"`+PasskeySentinel+`"}]}`), 0o600)

	ino, size, mtime := statTriple(t, env.MKVPath)
	freezeBody := "SOURCE_ID\tphysical_N5_disk\tRELEASE\taudio_state\tstatus\tMKV_INO\tMKV_SIZE\tMKV_MTIME\tTMDB_ID\n" +
		strings.Join([]string{"Synthetic.Source", Disk, Release, "eng,pol", "OK", ino, size, mtime, TMDBID}, "\t") + "\n"
	env.FreezePath = filepath.Join(root, "freeze", "zulea-frozen-names-20261104-000000.tsv")
	write(t, env.FreezePath, []byte(freezeBody), 0o400)
	freezeSum := sha256.Sum256([]byte(freezeBody))
	env.FreezeSHA256 = hex.EncodeToString(freezeSum[:])

	env.TitlesPath = filepath.Join(root, "tmdb-titles.tsv")
	write(t, env.TitlesPath, []byte("TMDB_ID\toriginal_language\ttitle_pl\ttitle_orig\ttitle_en\n"+
		TMDBID+"\ten\tPrzykładowy film\tExample Film\tExample Film\n"), 0o644)

	pieceLength := 16
	total := len(env.MKVBytes) + len(env.NFOBytes)
	pieces := (total + pieceLength - 1) / pieceLength
	info := map[string]any{
		"files": []any{
			map[string]any{"length": len(env.MKVBytes), "path": []any{Release + ".mkv"}},
			map[string]any{"length": len(env.NFOBytes), "path": []any{Release + ".nfo"}},
		},
		"name":         Release,
		"piece length": pieceLength,
		"pieces":       strings.Repeat("P", pieces*20),
		"private":      1,
	}
	for k, v := range opts.TorrentInfoExtra {
		info[k] = v
	}
	torrent := Encode(map[string]any{
		"announce":      "https://tracker.example.invalid/announce/" + PasskeySentinel,
		"created by":    "zulea-tm-fixture",
		"creation date": 1700000000,
		"info":          info,
	})
	env.TorrentPath = filepath.Join(env.TorrentDir, Release+".ptt.upload.torrent")
	write(t, env.TorrentPath, torrent, 0o600)
	hash := sha1.Sum(Encode(info)) //nolint:gosec // infohash fixture
	env.InfoHash = hex.EncodeToString(hash[:])
	torrentSum := sha256.Sum256(torrent)
	nfoSum := sha256.Sum256(env.NFOBytes)
	nIno, nSize, nMtime := statTriple(t, env.NFOPath)

	status := opts.ManifestPTTStatus
	if status == "" {
		status = "APPLY_OK"
	}
	manifest := "# zulea-tm-batch 0.10.0\n# TRYB=APPLY\n# FREEZE_SHA=" + env.FreezeSHA256 + "\n" +
		"RELEASE\tSOURCE_ID\tTMDB_ID\tMKV_INO\tMKV_SIZE\tMKV_MTIME\tNFO_INO\tNFO_SIZE\tNFO_MTIME\tNFO_SHA256\tINFO_HASH\t" +
		"PTT_STATUS\tPTT_TORRENT_SHA256\tCM_STATUS\tCM_TORRENT_SHA256\tWYNIK\tPOWOD\n" +
		strings.Join([]string{
			Release, "Synthetic.Source", TMDBID, ino, size, mtime, nIno, nSize, nMtime,
			hex.EncodeToString(nfoSum[:]), env.InfoHash, status, hex.EncodeToString(torrentSum[:]),
			"APPLY_OK", strings.Repeat("0", 64), "OK", "",
		}, "\t") + "\n# WYNIK=OK\n"
	env.ManifestPath = filepath.Join(root, "runs", "run-1", "manifest-20261104-000000.tsv")
	write(t, env.ManifestPath, []byte(manifest), 0o600)
	return env
}

// DiskCheck returns the W-2 disk check matching the fixture layout.
func (e *Env) DiskCheck() freeze.DiskCheck {
	return freeze.DiskCheck{
		Disk:      Disk,
		Mode:      freeze.DiskModePath,
		MountBase: e.MountBase,
	}
}
