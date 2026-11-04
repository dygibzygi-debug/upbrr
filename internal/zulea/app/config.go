// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package app wires the Zulea uploader: configuration, dry-run planning and
// reports. Stage 1 offers dry-run only; there is no upload entry point.
package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/autobrr/upbrr/internal/pathing"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
)

// Config is the uploader configuration. It holds no secret values: API keys
// live in separate private files referenced by path.
type Config struct {
	LibraryRoot  string `json:"library_root"`
	FreezePath   string `json:"freeze_path"`
	FreezeSHA256 string `json:"freeze_sha256"`
	TitlesPath   string `json:"titles_path"`
	ManifestPath string `json:"manifest_path"`
	TMOutputDir  string `json:"tm_output_dir"`
	WorkDir      string `json:"work_dir"`
	// TMDBKeyFile holds the TMDB v3 API key (mode 0600). Required for PTT (decision D-2).
	TMDBKeyFile string `json:"tmdb_key_file"`
	// TMDBBaseURL overrides the TMDB API root (tests only); empty uses the public API.
	TMDBBaseURL string `json:"tmdb_base_url,omitempty"`
	// Trackers lists enabled trackers. Stage 1 supports only "PTT"; empty means none.
	Trackers []string  `json:"trackers"`
	PTT      PTTConfig `json:"ptt"`
}

// PTTConfig holds PTT form choices.
type PTTConfig struct {
	// APIKeyFile holds the PTT API key (mode 0600). Optional for dry-run.
	APIKeyFile      string            `json:"api_key_file,omitempty"`
	Anonymous       bool              `json:"anonymous"`
	PersonalRelease bool              `json:"personal_release"`
	ModQueue        bool              `json:"mod_queue"`
	Taxonomy        *pttplan.Taxonomy `json:"taxonomy,omitempty"`
}

// LoadConfig reads a JSON config file that must be private (0600 or stricter)
// and rejects unknown fields.
func LoadConfig(path string) (Config, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("config: must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("config: mode %04o is too open (0600 required)", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks required fields and the read-only library boundary.
func (c Config) Validate() error {
	required := map[string]string{
		"library_root":  c.LibraryRoot,
		"freeze_path":   c.FreezePath,
		"freeze_sha256": c.FreezeSHA256,
		"titles_path":   c.TitlesPath,
		"manifest_path": c.ManifestPath,
		"tm_output_dir": c.TMOutputDir,
		"work_dir":      c.WorkDir,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("config: %s is required", name)
		}
	}
	for name, value := range map[string]string{"library_root": c.LibraryRoot, "work_dir": c.WorkDir} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("config: %s must be an absolute path", name)
		}
	}
	if pathing.IsWithinRoot(c.LibraryRoot, c.WorkDir) || pathing.IsWithinRoot(c.WorkDir, c.LibraryRoot) {
		return errors.New("config: work_dir and library_root must not overlap (the library is read only)")
	}
	for _, tracker := range c.Trackers {
		if !strings.EqualFold(strings.TrimSpace(tracker), "PTT") {
			return fmt.Errorf("config: tracker %q is not enabled in stage 1 (only PTT)", tracker)
		}
	}
	if c.PTTEnabled() && strings.TrimSpace(c.TMDBKeyFile) == "" {
		return errors.New("config: tmdb_key_file is required for PTT (decision D-2)")
	}
	return nil
}

// PTTEnabled reports whether PTT is explicitly enabled.
func (c Config) PTTEnabled() bool {
	for _, tracker := range c.Trackers {
		if strings.EqualFold(strings.TrimSpace(tracker), "PTT") {
			return true
		}
	}
	return false
}

// readKeyFile reads a private single-token key file.
func readKeyFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("key file: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("key file mode %04o is too open (0600 required)", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("key file: read: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return "", errors.New("key file must contain exactly one key")
	}
	return key, nil
}
