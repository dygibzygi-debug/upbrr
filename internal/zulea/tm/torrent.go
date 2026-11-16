// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package tm validates Torrent Maker (TM) output for one Zulea release and
// prepares the exact upload artifact.
//
// Decision D-1: the uploader sends the TM torrent unchanged. The info
// dictionary must be exactly files/name/piece length/pieces/private, private=1,
// name=REL, files = REL.mkv and REL.nfo with on-disk sizes, and no source.
// The upload copy keeps the info bytes verbatim (same infohash) and drops
// every key outside info, including the passkey-bearing announce.
// There is no fallback: a missing or non-conforming TM torrent is a STOP.
package tm

import (
	"crypto/sha1" //nolint:gosec // BitTorrent v1 infohash is defined as SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
)

// ErrTorrent marks a TM torrent that must not be uploaded (STOP for the release).
var ErrTorrent = errors.New("tm: torrent rejected")

// MaxTorrentBytes bounds the torrent file size.
const MaxTorrentBytes = 16 * 1024 * 1024

// InfoKeys is the exact key set of a TM info dictionary.
var InfoKeys = []string{"files", "name", "piece length", "pieces", "private"}

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTorrent, fmt.Sprintf(format, args...))
}

// File is one payload file of the torrent.
type File struct {
	Path   string
	Length int64
}

// Torrent is a validated TM torrent.
type Torrent struct {
	// SHA256 is the digest of the whole TM file (manifest PTT_TORRENT_SHA256).
	SHA256 string
	// InfoHash is the BitTorrent v1 infohash of the raw info bytes.
	InfoHash    string
	Name        string
	PieceLength int64
	Pieces      int
	Files       []File
	// TopLevelKeys lists keys outside info, for the dry-run report. Values are never exposed.
	TopLevelKeys []string
	infoRaw      []byte
}

// InfoBytes returns a copy of the raw info dictionary bytes.
func (t *Torrent) InfoBytes() []byte { return slices.Clone(t.infoRaw) }

// Expectation is what the release directory and freeze say the torrent must contain.
type Expectation struct {
	Release string
	// Sizes maps payload file name to its on-disk size.
	Sizes map[string]int64
}

// ReadFile reads a torrent file that must be a regular, non-symlink file.
func ReadFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, reject("stat %s: %v", filepath.Base(path), err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, reject("%s is not a regular file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, reject("open %s: %v", filepath.Base(path), err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxTorrentBytes+1))
	if err != nil {
		return nil, reject("read %s: %v", filepath.Base(path), err)
	}
	if len(data) > MaxTorrentBytes {
		return nil, reject("%s exceeds %d bytes", filepath.Base(path), MaxTorrentBytes)
	}
	return data, nil
}

// Parse validates raw TM torrent bytes against want.
func Parse(data []byte, want Expectation) (*Torrent, error) {
	root, err := decodeStrict(data)
	if err != nil {
		return nil, reject("%v", err)
	}
	top, ok := root.(*dict)
	if !ok {
		return nil, reject("root is not a dictionary")
	}
	infoValue, ok := top.values["info"]
	if !ok {
		return nil, reject("missing info dictionary")
	}
	info, ok := infoValue.(*dict)
	if !ok {
		return nil, reject("info is not a dictionary")
	}
	if !slices.Equal(info.keys, InfoKeys) {
		return nil, reject("info keys %q, expected exactly %q", info.keys, InfoKeys)
	}
	if private, ok := info.values["private"].(int64); !ok || private != 1 {
		return nil, reject("info.private must be integer 1")
	}
	name, ok := info.values["name"].([]byte)
	if !ok || string(name) != want.Release {
		return nil, reject("info.name differs from the release name")
	}
	pieceLength, ok := info.values["piece length"].(int64)
	if !ok || pieceLength <= 0 || pieceLength&(pieceLength-1) != 0 {
		return nil, reject("info.piece length must be a positive power of two")
	}
	pieces, ok := info.values["pieces"].([]byte)
	if !ok || len(pieces) == 0 || len(pieces)%sha1.Size != 0 {
		return nil, reject("info.pieces must be a non-empty multiple of 20 bytes")
	}
	files, total, err := parseFiles(info.values["files"])
	if err != nil {
		return nil, err
	}
	if err := checkFiles(files, want.Sizes); err != nil {
		return nil, err
	}
	expectedPieces := (total + pieceLength - 1) / pieceLength
	if int64(len(pieces)/sha1.Size) != expectedPieces {
		return nil, reject("piece count %d does not cover %d bytes at piece length %d", len(pieces)/sha1.Size, total, pieceLength)
	}
	infoRaw := top.raw["info"]
	hash := sha1.Sum(infoRaw) //nolint:gosec // BitTorrent v1 infohash.
	digest := sha256.Sum256(data)
	others := make([]string, 0, len(top.keys))
	for _, key := range top.keys {
		if key != "info" {
			others = append(others, key)
		}
	}
	return &Torrent{
		SHA256:       hex.EncodeToString(digest[:]),
		InfoHash:     hex.EncodeToString(hash[:]),
		Name:         string(name),
		PieceLength:  pieceLength,
		Pieces:       len(pieces) / sha1.Size,
		Files:        files,
		TopLevelKeys: others,
		infoRaw:      slices.Clone(infoRaw),
	}, nil
}

func parseFiles(v value) ([]File, int64, error) {
	list, ok := v.([]value)
	if !ok || len(list) == 0 {
		return nil, 0, reject("info.files must be a non-empty list")
	}
	files := make([]File, 0, len(list))
	var total int64
	for index, item := range list {
		entry, ok := item.(*dict)
		if !ok || !slices.Equal(entry.keys, []string{"length", "path"}) {
			return nil, 0, reject("info.files[%d] must have exactly length and path", index)
		}
		length, ok := entry.values["length"].(int64)
		if !ok || length < 0 {
			return nil, 0, reject("info.files[%d].length invalid", index)
		}
		parts, ok := entry.values["path"].([]value)
		if !ok || len(parts) != 1 {
			return nil, 0, reject("info.files[%d].path must be a single top-level file name", index)
		}
		part, ok := parts[0].([]byte)
		if !ok || len(part) == 0 {
			return nil, 0, reject("info.files[%d].path is empty", index)
		}
		files = append(files, File{Path: string(part), Length: length})
		total += length
	}
	return files, total, nil
}

func checkFiles(files []File, sizes map[string]int64) error {
	got := make([]string, 0, len(files))
	for _, f := range files {
		got = append(got, f.Path)
		size, ok := sizes[f.Path]
		if !ok {
			return reject("torrent contains unexpected file %s", f.Path)
		}
		if size != f.Length {
			return reject("file %s length %d differs from on-disk size %d", f.Path, f.Length, size)
		}
	}
	want := make([]string, 0, len(sizes))
	for name := range sizes {
		want = append(want, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		return reject("torrent files %q, expected exactly %q", got, want)
	}
	return nil
}

// UploadBytes returns the upload artifact: a root dictionary holding only the
// verbatim info bytes. The infohash equals the TM infohash and no announce,
// passkey, comment or creator survives.
func (t *Torrent) UploadBytes() []byte {
	out := make([]byte, 0, len(t.infoRaw)+12)
	out = append(out, "d4:info"...)
	out = append(out, t.infoRaw...)
	return append(out, 'e')
}

// WriteUploadCopy atomically writes UploadBytes to path with mode 0600 and
// verifies the written infohash.
func (t *Torrent) WriteUploadCopy(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("tm: create upload directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("tm: create temp upload copy: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tm: chmod upload copy: %w", err)
	}
	if _, err := tmp.Write(t.UploadBytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tm: write upload copy: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tm: sync upload copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tm: close upload copy: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("tm: publish upload copy: %w", err)
	}
	cleanup = false
	written, err := ReadFile(path)
	if err != nil {
		return err
	}
	hash, err := InfoHashOf(written)
	if err != nil {
		return err
	}
	if hash != t.InfoHash {
		return reject("upload copy infohash %s differs from TM %s", hash, t.InfoHash)
	}
	return nil
}

// InfoHashOf returns the v1 infohash of any canonical torrent bytes.
func InfoHashOf(data []byte) (string, error) {
	root, err := decodeStrict(data)
	if err != nil {
		return "", reject("%v", err)
	}
	top, ok := root.(*dict)
	if !ok {
		return "", reject("root is not a dictionary")
	}
	raw, ok := top.raw["info"]
	if !ok {
		return "", reject("missing info dictionary")
	}
	hash := sha1.Sum(raw) //nolint:gosec // BitTorrent v1 infohash.
	return hex.EncodeToString(hash[:]), nil
}

// FormatFiles renders the file list for reports.
func FormatFiles(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path+" ("+strconv.FormatInt(f.Length, 10)+" B)")
	}
	return out
}
