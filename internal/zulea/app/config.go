// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package app wires the Zulea uploader: configuration, dry-run planning,
// the gated canary upload and operator decisions (resolve, dupe-accept).
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
	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
)

// Config is the uploader configuration. It holds no secret values: API keys
// live in separate private files referenced by path.
type Config struct {
	LibraryRoot  string `json:"library_root"`
	FreezePath   string `json:"freeze_path"`
	FreezeSHA256 string `json:"freeze_sha256"`
	TitlesPath   string `json:"titles_path"`
	// One TM run: manifest_path + tm_output_dir.
	ManifestPath string `json:"manifest_path,omitempty"`
	TMOutputDir  string `json:"tm_output_dir,omitempty"`
	// All TM runs: tm_runs_dir (<RUN_ID>/manifest-*.tsv) + tm_output_base (<RUN_ID>/*.torrent).
	TMRunsDir    string `json:"tm_runs_dir,omitempty"`
	TMOutputBase string `json:"tm_output_base,omitempty"`
	WorkDir      string `json:"work_dir"`
	// TMDBKeyFile holds the TMDB v3 API key (mode 0600). Required for PTT (decision D-2).
	TMDBKeyFile string `json:"tmdb_key_file"`
	// TMDBBaseURL overrides the TMDB API root (tests only); empty uses the public API.
	TMDBBaseURL string `json:"tmdb_base_url,omitempty"`
	// PhysicalDisk is the array disk every freeze record must name (W-2), e.g. "disk1".
	PhysicalDisk string `json:"physical_disk"`
	// DiskCheckMode is "path" (library read via /mnt/<disk>/...) or "unraid_xattr".
	DiskCheckMode string `json:"disk_check_mode"`
	// MountBase overrides the /mnt array mount root (tests only).
	MountBase string          `json:"mount_base,omitempty"`
	MediaInfo MediaInfoConfig `json:"mediainfo"`
	// Trackers lists enabled trackers. Stage 1 supports only "PTT"; empty means none.
	Trackers []string  `json:"trackers"`
	PTT      PTTConfig `json:"ptt"`
	// QBittorrent enables the separate seeding step (zulea-upbrr seed).
	QBittorrent *QBitConfig `json:"qbittorrent,omitempty"`
	// Notifications enables summary messages (zulea-upbrr summary --notify).
	Notifications *NotifyConfig `json:"notifications,omitempty"`
}

// NotifyConfig holds notification targets; secrets live in 0600 files.
type NotifyConfig struct {
	DiscordWebhookFile string `json:"discord_webhook_file,omitempty"`
	TelegramTokenFile  string `json:"telegram_token_file,omitempty"`
	TelegramChatID     string `json:"telegram_chat_id,omitempty"`
}

// QBitConfig configures the seeding step. The password lives in a 0600 file.
type QBitConfig struct {
	Host         string `json:"host"`
	Username     string `json:"username,omitempty"`
	PasswordFile string `json:"password_file,omitempty"`
	// SavePath is the library root as qBittorrent sees it (its own mount).
	SavePath string `json:"save_path"`
	Category string `json:"category,omitempty"`
	Tags     string `json:"tags,omitempty"`
}

// MediaInfo sources (decision D-6).
const (
	MediaInfoCLI   = "cli"
	MediaInfoFile  = "file"
	MediaInfoUpbrr = "upbrr"
)

// MediaInfoConfig selects the MediaInfo text source.
type MediaInfoConfig struct {
	// Source is "cli" (run the MediaInfo binary), "file" (ready <REL>.mediainfo.txt
	// in Dir) or "upbrr" (go-mediainfo).
	Source string `json:"source"`
	// Binary is the mediainfo executable for "cli".
	Binary string `json:"binary,omitempty"`
	// Dir holds ready reports for "file"; it must be outside the library.
	Dir string `json:"dir,omitempty"`
}

// DiskCheck returns the W-2 physical disk check.
func (c Config) DiskCheck() freeze.DiskCheck {
	return freeze.DiskCheck{
		Disk:      c.PhysicalDisk,
		Mode:      c.DiskCheckMode,
		MountBase: c.MountBase,
	}
}

// PTTConfig holds PTT form choices.
type PTTConfig struct {
	// APIKeyFile holds the PTT API key (mode 0600). Optional for dry-run.
	APIKeyFile string `json:"api_key_file,omitempty"`
	Anonymous  bool   `json:"anonymous"`
	// PersonalRelease must be decided explicitly (D-5); null blocks canary readiness.
	PersonalRelease *bool `json:"personal_release"`
	// ModQueue opts into the moderation queue; required true for the canary (D-5).
	ModQueue bool              `json:"mod_queue"`
	Taxonomy *pttplan.Taxonomy `json:"taxonomy,omitempty"`
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
		"work_dir":      c.WorkDir,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("config: %s is required", name)
		}
	}
	single := c.ManifestPath != "" && c.TMOutputDir != ""
	multi := c.TMRunsDir != "" && c.TMOutputBase != ""
	partial := (c.ManifestPath != "") != (c.TMOutputDir != "") || (c.TMRunsDir != "") != (c.TMOutputBase != "")
	if single == multi || partial {
		return errors.New("config: set either manifest_path + tm_output_dir (one TM run) or tm_runs_dir + tm_output_base (all runs)")
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
	if err := c.DiskCheck().Validate(); err != nil {
		return fmt.Errorf("config: physical_disk/disk_check_mode: %w", err)
	}
	if q := c.QBittorrent; q != nil {
		if strings.TrimSpace(q.Host) == "" || strings.TrimSpace(q.SavePath) == "" {
			return errors.New("config: qbittorrent.host and qbittorrent.save_path are required")
		}
		if (q.Username == "") != (q.PasswordFile == "") {
			return errors.New("config: qbittorrent.username and qbittorrent.password_file go together")
		}
	}
	if n := c.Notifications; n != nil {
		if (n.TelegramTokenFile == "") != (n.TelegramChatID == "") {
			return errors.New("config: notifications.telegram_token_file and telegram_chat_id go together")
		}
		if n.DiscordWebhookFile == "" && n.TelegramTokenFile == "" {
			return errors.New("config: notifications needs discord_webhook_file or telegram_token_file")
		}
	}
	switch c.MediaInfo.Source {
	case MediaInfoCLI:
		if strings.TrimSpace(c.MediaInfo.Binary) == "" {
			return errors.New("config: mediainfo.binary is required for source \"cli\"")
		}
	case MediaInfoFile:
		if !filepath.IsAbs(c.MediaInfo.Dir) || pathing.IsWithinRoot(c.LibraryRoot, c.MediaInfo.Dir) {
			return errors.New("config: mediainfo.dir must be an absolute path outside the library")
		}
	case MediaInfoUpbrr:
	default:
		return errors.New("config: mediainfo.source must be \"cli\", \"file\" or \"upbrr\" (decision D-6)")
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
	info, err := os.Lstat(path) //nolint:gosec // Operator-configured key file path; symlinks and loose modes are refused below.
	if err != nil {
		return "", fmt.Errorf("key file: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("key file mode %04o is too open (0600 required)", info.Mode().Perm())
	}
	data, err := os.ReadFile(path) //nolint:gosec // Operator-configured key file path, checked above.
	if err != nil {
		return "", fmt.Errorf("key file: read: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return "", errors.New("key file must contain exactly one key")
	}
	return key, nil
}

// ApplyTaxonomy writes tax into the config file at path, keeping it 0600 and
// valid. The previous file is replaced atomically.
func ApplyTaxonomy(path string, tax pttplan.Taxonomy) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	cfg.PTT.Taxonomy = &tax
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("config: temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: chmod: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("config: replace: %w", err)
	}
	return nil
}
