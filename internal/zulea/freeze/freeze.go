// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package freeze reads the Zulea name freeze, the single source of truth for
// release names and MKV identity, and verifies a library MKV against it.
//
// The checks mirror the Torrent Maker batch wrapper (load_freeze and
// precheck_release): exact header, nine fields per row, exactly one record per
// release, status OK, and stat identity (inode, size, mtime seconds).
package freeze

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Header is the exact freeze header.
var Header = []string{
	"SOURCE_ID", "physical_N5_disk", "RELEASE", "audio_state", "status",
	"MKV_INO", "MKV_SIZE", "MKV_MTIME", "TMDB_ID",
}

// StatusOK is the only status eligible for upload.
const StatusOK = "OK"

// MaxBytes bounds the freeze file size.
const MaxBytes = 64 * 1024 * 1024

var (
	uintPattern = regexp.MustCompile(`^[0-9]+$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ErrNotEligible marks a release that must not be uploaded (STOP for that release).
var ErrNotEligible = errors.New("freeze: release not eligible")

// Record is one freeze row. Numeric fields keep their textual form so the
// comparison is exact, like the shell wrapper's string comparison.
type Record struct {
	SourceID   string
	Disk       string
	Release    string
	AudioState string
	Status     string
	MKVIno     string
	MKVSize    string
	MKVMtime   string
	TMDBID     string
}

// Freeze is a parsed freeze file.
type Freeze struct {
	SHA256  string
	records map[string][]Record
	count   int
}

// Len returns the number of data rows.
func (f *Freeze) Len() int { return f.count }

// Load reads path, verifies its SHA-256 against expectSHA (required, lowercase
// hex) and parses it. Symbolic links and non-regular files are rejected.
func Load(path string, expectSHA string) (*Freeze, error) {
	expectSHA = strings.TrimSpace(expectSHA)
	if !shaPattern.MatchString(expectSHA) {
		return nil, errors.New("freeze: expected SHA-256 is required (64 lowercase hex characters)")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("freeze: stat: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("freeze: file must be a regular file, not a symbolic link")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("freeze: open: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("freeze: file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("freeze: read: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, errors.New("freeze: file too large")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != expectSHA {
		return nil, fmt.Errorf("freeze: SHA-256 %s != expected %s", got, expectSHA)
	}
	parsed, err := Parse(data)
	if err != nil {
		return nil, err
	}
	parsed.SHA256 = got
	return parsed, nil
}

// Parse parses freeze bytes without the SHA check.
func Parse(data []byte) (*Freeze, error) {
	text := string(data)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, errors.New("freeze: empty file")
	}
	header := strings.ReplaceAll(lines[0], "\r", "")
	if !slices.Equal(strings.Split(header, "\t"), Header) {
		return nil, errors.New("freeze: header differs from expected")
	}
	out := &Freeze{records: make(map[string][]Record, len(lines))}
	for index, raw := range lines[1:] {
		fields := strings.Split(strings.TrimSuffix(raw, "\r"), "\t")
		if len(fields) != len(Header) {
			return nil, fmt.Errorf("freeze: row %d has %d fields (expected %d)", index+2, len(fields), len(Header))
		}
		rec := Record{
			SourceID:   fields[0],
			Disk:       fields[1],
			Release:    fields[2],
			AudioState: fields[3],
			Status:     fields[4],
			MKVIno:     fields[5],
			MKVSize:    fields[6],
			MKVMtime:   fields[7],
			TMDBID:     fields[8],
		}
		out.records[rec.Release] = append(out.records[rec.Release], rec)
		out.count++
	}
	return out, nil
}

// Eligible returns the single OK record for release, or ErrNotEligible with the reason.
func (f *Freeze) Eligible(release string) (Record, error) {
	records := f.records[release]
	if len(records) != 1 {
		return Record{}, fmt.Errorf("%w: %d freeze records for release (required 1)", ErrNotEligible, len(records))
	}
	rec := records[0]
	if rec.Status != StatusOK {
		return Record{}, fmt.Errorf("%w: status %s (only %s allowed)", ErrNotEligible, rec.Status, StatusOK)
	}
	if !uintPattern.MatchString(rec.MKVIno) || !uintPattern.MatchString(rec.MKVSize) || !uintPattern.MatchString(rec.MKVMtime) {
		return Record{}, fmt.Errorf("%w: MKV_INO/MKV_SIZE/MKV_MTIME are not unsigned integers", ErrNotEligible)
	}
	return rec, nil
}

// Identity is the stat triple the freeze records for an MKV.
type Identity struct {
	Ino   uint64
	Size  int64
	Mtime int64
}

func (id Identity) String() string {
	return fmt.Sprintf("%d/%d/%d", id.Ino, id.Size, id.Mtime)
}

// StatIdentity returns the inode, size and mtime seconds of a regular,
// non-symlink file at path.
func StatIdentity(path string) (Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, fmt.Errorf("freeze: stat: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Identity{}, errors.New("freeze: not a regular file")
	}
	ino, err := inodeOf(info)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		Ino:   ino,
		Size:  info.Size(),
		Mtime: info.ModTime().Unix(),
	}, nil
}

// VerifyMKV checks that the file at path has exactly the identity recorded in rec.
func VerifyMKV(rec Record, path string) (Identity, error) {
	got, err := StatIdentity(path)
	if err != nil {
		return Identity{}, err
	}
	want := rec.MKVIno + "/" + rec.MKVSize + "/" + rec.MKVMtime
	if strconv.FormatUint(got.Ino, 10)+"/"+strconv.FormatInt(got.Size, 10)+"/"+strconv.FormatInt(got.Mtime, 10) != want {
		return got, fmt.Errorf("%w: MKV differs from freeze (ino/size/mtime %s vs %s)", ErrNotEligible, got, want)
	}
	return got, nil
}
