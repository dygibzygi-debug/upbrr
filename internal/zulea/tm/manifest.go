// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
)

// ManifestHeader is the exact column header of a TM batch manifest.
var ManifestHeader = []string{
	"RELEASE", "SOURCE_ID", "TMDB_ID", "MKV_INO", "MKV_SIZE", "MKV_MTIME",
	"NFO_INO", "NFO_SIZE", "NFO_MTIME", "NFO_SHA256", "INFO_HASH",
	"PTT_STATUS", "PTT_TORRENT_SHA256", "CM_STATUS", "CM_TORRENT_SHA256", "WYNIK", "POWOD",
}

// MaxManifestBytes bounds the manifest size.
const MaxManifestBytes = 32 * 1024 * 1024

var (
	hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ErrManifest marks a manifest that does not prove the release (STOP).
var ErrManifest = errors.New("tm: manifest does not prove release")

// ManifestRow is one manifest data row.
type ManifestRow struct {
	Release, SourceID, TMDBID   string
	MKVIno, MKVSize, MKVMtime   string
	NFOIno, NFOSize, NFOMtime   string
	NFOSHA256, InfoHash         string
	PTTStatus, PTTTorrentSHA256 string
	CMStatus, CMTorrentSHA256   string
	Result, Reason              string
}

// Manifest is a parsed TM batch manifest.
type Manifest struct {
	// Meta holds "# KEY=value" comment lines, e.g. TRYB, FREEZE_SHA, OUTPUT_DIR.
	Meta map[string]string
	rows map[string][]ManifestRow
}

// LoadManifest reads a manifest file (regular, non-symlink).
func LoadManifest(path string) (*Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("tm: stat manifest: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("tm: manifest must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tm: open manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("tm: read manifest: %w", err)
	}
	if len(data) > MaxManifestBytes {
		return nil, errors.New("tm: manifest too large")
	}
	return ParseManifest(data)
}

// ParseManifest parses manifest bytes.
func ParseManifest(data []byte) (*Manifest, error) {
	m := &Manifest{Meta: map[string]string{}, rows: map[string][]ManifestRow{}}
	headerSeen := false
	for index, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			key, val, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#")), "=")
			if ok {
				m.Meta[strings.TrimSpace(key)] = val
			}
			continue
		}
		fields := strings.Split(line, "\t")
		if !headerSeen {
			if !slices.Equal(fields, ManifestHeader) {
				return nil, errors.New("tm: manifest header differs from expected")
			}
			headerSeen = true
			continue
		}
		if len(fields) != len(ManifestHeader) {
			return nil, fmt.Errorf("tm: manifest line %d has %d fields", index+1, len(fields))
		}
		r := ManifestRow{
			Release:          fields[0],
			SourceID:         fields[1],
			TMDBID:           fields[2],
			MKVIno:           fields[3],
			MKVSize:          fields[4],
			MKVMtime:         fields[5],
			NFOIno:           fields[6],
			NFOSize:          fields[7],
			NFOMtime:         fields[8],
			NFOSHA256:        fields[9],
			InfoHash:         fields[10],
			PTTStatus:        fields[11],
			PTTTorrentSHA256: fields[12],
			CMStatus:         fields[13],
			CMTorrentSHA256:  fields[14],
			Result:           fields[15],
			Reason:           fields[16],
		}
		m.rows[r.Release] = append(m.rows[r.Release], r)
	}
	if !headerSeen {
		return nil, errors.New("tm: manifest has no header")
	}
	return m, nil
}

// ProfileStatus returns the status and torrent SHA-256 of a TM profile column.
func (r ManifestRow) ProfileStatus(profile string) (string, string, error) {
	switch profile {
	case "ptt":
		return r.PTTStatus, r.PTTTorrentSHA256, nil
	case "cinemamovies":
		return r.CMStatus, r.CMTorrentSHA256, nil
	default:
		return "", "", fmt.Errorf("tm: unknown manifest profile %q", profile)
	}
}

// Proven returns the single OK row for release whose profile was applied.
func (m *Manifest) Proven(release string, profile string) (ManifestRow, error) {
	if m.Meta["TRYB"] != "APPLY" {
		return ManifestRow{}, fmt.Errorf("%w: manifest mode %q (APPLY required)", ErrManifest, m.Meta["TRYB"])
	}
	rows := m.rows[release]
	if len(rows) != 1 {
		return ManifestRow{}, fmt.Errorf("%w: %d manifest rows for release (required 1)", ErrManifest, len(rows))
	}
	r := rows[0]
	if r.Result != "OK" {
		return ManifestRow{}, fmt.Errorf("%w: manifest result %s", ErrManifest, r.Result)
	}
	status, digest, err := r.ProfileStatus(profile)
	if err != nil {
		return ManifestRow{}, err
	}
	if status != "APPLY_OK" && status != "ALREADY_OK" {
		return ManifestRow{}, fmt.Errorf("%w: %s status %s", ErrManifest, profile, status)
	}
	if !hex40.MatchString(r.InfoHash) || !hex64.MatchString(digest) || !hex64.MatchString(r.NFOSHA256) {
		return ManifestRow{}, fmt.Errorf("%w: manifest digests malformed", ErrManifest)
	}
	return r, nil
}
