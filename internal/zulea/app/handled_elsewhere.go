// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
)

const (
	handledElsewhereDefaultPath = "/inputs/handled-elsewhere.json"
	StatusHandledElsewhere      = "HANDLED_ELSEWHERE"
)

// HandledElsewhereEntry records a release already sent outside zulea-upbrr.
// The input file is read-only and contains no credentials.
type HandledElsewhereEntry struct {
	Release   string `json:"release"`
	Tracker   string `json:"tracker"`
	TorrentID *int   `json:"torrent_id,omitempty"`
	Tool      string `json:"tool"`
	Added     string `json:"added"`
	Note      string `json:"note,omitempty"`
}

type handledElsewhereFile struct {
	SchemaVersion int                     `json:"schema_version"`
	Entries       []HandledElsewhereEntry `json:"entries"`
}

// HandledElsewhereValidation is the check-config result for the read-only list.
type HandledElsewhereValidation struct {
	Entries  int      `json:"entries"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *Runner) handledElsewherePath() string {
	if r.HandledElsewherePath != "" {
		return r.HandledElsewherePath
	}
	return handledElsewhereDefaultPath
}

func (r *Runner) loadHandledElsewhere() (map[string]HandledElsewhereEntry, error) {
	path := r.handledElsewherePath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]HandledElsewhereEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("handled-elsewhere: %w", err)
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	var doc handledElsewhereFile
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("handled-elsewhere: invalid JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("handled-elsewhere: JSON must contain exactly one document")
	}
	if doc.SchemaVersion != 1 {
		return nil, fmt.Errorf("handled-elsewhere: schema_version must be 1 (got %d)", doc.SchemaVersion)
	}
	if doc.Entries == nil {
		return nil, errors.New("handled-elsewhere: entries must be an array")
	}
	out := make(map[string]HandledElsewhereEntry, len(doc.Entries))
	for i, entry := range doc.Entries {
		if !validName(entry.Release) {
			return nil, fmt.Errorf("handled-elsewhere: entry %d has an invalid release", i+1)
		}
		if entry.Tracker != "PTT" {
			return nil, fmt.Errorf("handled-elsewhere: entry %d tracker must be PTT", i+1)
		}
		switch entry.Tool {
		case "upbrr", "manual", "other":
		default:
			return nil, fmt.Errorf("handled-elsewhere: entry %d tool must be upbrr, manual or other", i+1)
		}
		if entry.TorrentID != nil && *entry.TorrentID <= 0 {
			return nil, fmt.Errorf("handled-elsewhere: entry %d torrent_id must be positive", i+1)
		}
		added, err := time.Parse("2006-01-02", entry.Added)
		if err != nil || added.Format("2006-01-02") != entry.Added {
			return nil, fmt.Errorf("handled-elsewhere: entry %d added must be YYYY-MM-DD", i+1)
		}
		if strings.ContainsAny(entry.Note, "\r\n") {
			return nil, fmt.Errorf("handled-elsewhere: entry %d note must be one line", i+1)
		}
		if _, exists := out[entry.Release]; exists {
			return nil, fmt.Errorf("handled-elsewhere: duplicate release %q", entry.Release)
		}
		out[entry.Release] = entry
	}
	return out, nil
}

func handledElsewhereReason(entry HandledElsewhereEntry) string {
	detail := entry.Tool
	if entry.TorrentID != nil {
		detail += fmt.Sprintf(", #%d", *entry.TorrentID)
	}
	return fmt.Sprintf("obsłużone innym narzędziem (%s)", detail)
}

func (r *Runner) handledElsewhere(release string) (*HandledElsewhereEntry, error) {
	entries, err := r.loadHandledElsewhere()
	if err != nil {
		return nil, err
	}
	entry, ok := entries[release]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (r *Runner) refuseHandledElsewhere(release string) error {
	entry, err := r.handledElsewhere(release)
	if err != nil {
		return err
	}
	if entry != nil {
		return errors.New(handledElsewhereReason(*entry))
	}
	return nil
}

// ValidateHandledElsewhere checks contradictions and stale names without any
// network access. A receipt is authoritative evidence that zulea-upbrr handled
// the release, so listing the same release here is a configuration error.
func (r *Runner) ValidateHandledElsewhere() (HandledElsewhereValidation, error) {
	entries, err := r.loadHandledElsewhere()
	if err != nil {
		return HandledElsewhereValidation{}, err
	}
	result := HandledElsewhereValidation{Entries: len(entries)}
	if len(entries) == 0 {
		return result, nil
	}
	frozen, err := freeze.Load(r.Config.FreezePath, r.Config.FreezeSHA256)
	if err != nil {
		return result, fmt.Errorf("handled-elsewhere: freeze: %w", err)
	}
	known := map[string]bool{}
	releases, _ := frozen.Releases()
	for _, release := range releases {
		known[release] = true
	}
	names := make([]string, 0, len(entries))
	for release := range entries {
		names = append(names, release)
	}
	sort.Strings(names)
	for _, release := range names {
		if _, statErr := os.Stat(r.receiptPath(release)); statErr == nil {
			return result, fmt.Errorf("handled-elsewhere: release %q also has a zulea-upbrr receipt", release)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return result, fmt.Errorf("handled-elsewhere: receipt %q: %w", release, statErr)
		}
		_, libraryErr := os.Stat(filepath.Join(r.Config.LibraryRoot, release))
		inLibrary := libraryErr == nil
		if libraryErr != nil && !errors.Is(libraryErr, os.ErrNotExist) {
			return result, fmt.Errorf("handled-elsewhere: library %q: %w", release, libraryErr)
		}
		if !known[release] {
			result.Warnings = append(result.Warnings, fmt.Sprintf("handled-elsewhere: %s is absent from the freeze", release))
		}
		if !inLibrary {
			result.Warnings = append(result.Warnings, fmt.Sprintf("handled-elsewhere: %s is absent from the library", release))
		}
	}
	return result, nil
}
