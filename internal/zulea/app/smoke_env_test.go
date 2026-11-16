// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

// TestWriteSmokeEnvironment materializes a synthetic environment and config
// for container smoke tests when ZULEA_SMOKE_DIR is set. It never runs in CI.
func TestWriteSmokeEnvironment(t *testing.T) {
	dir := os.Getenv("ZULEA_SMOKE_DIR")
	if dir == "" {
		t.Skip("smoke environment not requested")
	}
	env := zuleatest.NewAt(t, dir, zuleatest.Options{})
	cfg := Config{
		LibraryRoot:   env.LibraryRoot,
		FreezePath:    env.FreezePath,
		FreezeSHA256:  env.FreezeSHA256,
		TitlesPath:    env.TitlesPath,
		TMRunsDir:     filepath.Dir(filepath.Dir(env.ManifestPath)),
		TMOutputBase:  filepath.Dir(env.TorrentDir),
		WorkDir:       filepath.Join(filepath.Dir(dir), "work"),
		TMDBKeyFile:   filepath.Join(dir, "tmdb.key"),
		PhysicalDisk:  zuleatest.Disk,
		DiskCheckMode: freeze.DiskModePath,
		MountBase:     env.MountBase,
		MediaInfo:     MediaInfoConfig{Source: MediaInfoCLI, Binary: "/usr/bin/mediainfo"},
		Trackers:      []string{"PTT"},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zulea.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
