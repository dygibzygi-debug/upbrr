// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package tm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Located is the proof for one release: its manifest row and the directory
// holding that run's torrents.
type Located struct {
	Row        ManifestRow
	TorrentDir string
	// RunID and Manifest identify the proving TM run (empty in single-run mode).
	RunID    string
	Manifest string
	// FreezeSHA is the FREEZE_SHA recorded by the proving manifest.
	FreezeSHA string
}

// Catalog finds the TM proof for a release.
type Catalog interface {
	Locate(release string, profile string) (Located, error)
}

// SingleRun is one manifest with its output directory.
type SingleRun struct {
	Manifest   *Manifest
	TorrentDir string
}

// Locate implements Catalog.
func (s SingleRun) Locate(release string, profile string) (Located, error) {
	row, err := s.Manifest.Proven(release, profile)
	if err != nil {
		return Located{}, err
	}
	return Located{
		Row:        row,
		TorrentDir: s.TorrentDir,
		FreezeSHA:  s.Manifest.Meta["FREEZE_SHA"],
	}, nil
}

var (
	runIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	manifestPattern = regexp.MustCompile(`^manifest-[0-9]{8}-[0-9]{6}\.tsv$`)
)

type runManifest struct {
	runID    string
	name     string
	manifest *Manifest
}

// MultiRun indexes every TM batch run: <runsDir>/<RUN_ID>/manifest-*.tsv,
// with torrents in <outputBase>/<RUN_ID>/ (the wrapper's RUNS_BASE and
// OUTPUT_BASE layout).
type MultiRun struct {
	outputBase string
	runs       []runManifest
}

// LoadMultiRun reads every manifest below runsDir. A malformed manifest fails
// the load: the chain of evidence must not silently skip a run.
func LoadMultiRun(runsDir, outputBase string) (*MultiRun, error) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, fmt.Errorf("tm: list runs: %w", err)
	}
	out := &MultiRun{outputBase: outputBase}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !runIDPattern.MatchString(entry.Name()) ||
			entry.Name() == "." || entry.Name() == ".." {
			continue
		}
		runDir := filepath.Join(runsDir, entry.Name())
		files, err := os.ReadDir(runDir)
		if err != nil {
			return nil, fmt.Errorf("tm: list run %s: %w", entry.Name(), err)
		}
		for _, file := range files {
			if !manifestPattern.MatchString(file.Name()) {
				continue
			}
			manifest, err := LoadManifest(filepath.Join(runDir, file.Name()))
			if err != nil {
				return nil, fmt.Errorf("tm: run %s manifest %s: %w", entry.Name(), file.Name(), err)
			}
			if id := manifest.Meta["RUN_ID"]; id != "" && id != entry.Name() {
				return nil, fmt.Errorf("tm: run %s manifest %s records RUN_ID %s", entry.Name(), file.Name(), id)
			}
			out.runs = append(out.runs, runManifest{
				runID:    entry.Name(),
				name:     file.Name(),
				manifest: manifest,
			})
		}
	}
	if len(out.runs) == 0 {
		return nil, errors.New("tm: no manifests found in runs directory")
	}
	// Newest last: manifest names carry the start stamp.
	sort.SliceStable(out.runs, func(i, j int) bool {
		if out.runs[i].name != out.runs[j].name {
			return out.runs[i].name < out.runs[j].name
		}
		return out.runs[i].runID < out.runs[j].runID
	})
	return out, nil
}

// Runs returns the number of loaded manifests.
func (m *MultiRun) Runs() int { return len(m.runs) }

// Locate implements Catalog: the newest run that proves the release wins,
// and every proving run must agree on the infohash.
func (m *MultiRun) Locate(release string, profile string) (Located, error) {
	var proofs []Located
	var lastErr error
	for _, run := range m.runs {
		row, err := run.manifest.Proven(release, profile)
		if err != nil {
			lastErr = err
			continue
		}
		proofs = append(proofs, Located{
			Row:        row,
			TorrentDir: filepath.Join(m.outputBase, run.runID),
			RunID:      run.runID,
			Manifest:   run.name,
			FreezeSHA:  run.manifest.Meta["FREEZE_SHA"],
		})
	}
	if len(proofs) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("%w: release absent from every manifest", ErrManifest)
		}
		return Located{}, fmt.Errorf("%w: no TM run proves the release (last reason: %w)", ErrManifest, lastErr)
	}
	hashes := map[string]struct{}{}
	for _, p := range proofs {
		hashes[p.Row.InfoHash] = struct{}{}
	}
	if len(hashes) > 1 {
		runs := make([]string, 0, len(proofs))
		for _, p := range proofs {
			runs = append(runs, p.RunID+"="+p.Row.InfoHash[:8])
		}
		return Located{}, fmt.Errorf("%w: TM runs disagree on the infohash (%s)", ErrManifest, strings.Join(runs, ", "))
	}
	return proofs[len(proofs)-1], nil
}
