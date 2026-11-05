// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

// Seeding states reported by SeedStatus.
const (
	SeedSeeding  = "SEEDING"
	SeedStopped  = "STOPPED_100"
	SeedChecking = "CHECKING"
	SeedPartial  = "INCOMPLETE"
	SeedError    = "ERROR"
	SeedAbsent   = "ABSENT"
	SeedUnknown  = "UNKNOWN"
)

// SeedStatusResult is the qBittorrent state of one accepted upload.
type SeedStatusResult struct {
	Release  string  `json:"release"`
	InfoHash string  `json:"info_hash,omitempty"`
	Seed     string  `json:"seed"`
	State    string  `json:"qbittorrent_state,omitempty"`
	Progress float64 `json:"progress"`
	Error    string  `json:"error,omitempty"`
}

func classifySeed(t *seed.Torrent) string {
	switch {
	case t == nil:
		return SeedAbsent
	case t.State == "error" || t.State == "missingFiles":
		return SeedError
	case strings.HasPrefix(t.State, "checking") || t.State == "moving" || t.State == "allocating":
		return SeedChecking
	case t.Progress < 1:
		return SeedPartial
	case t.State == "pausedUP" || t.State == "stoppedUP":
		return SeedStopped
	case t.State == "uploading" || t.State == "stalledUP" || t.State == "queuedUP" || t.State == "forcedUP":
		return SeedSeeding
	default:
		return SeedUnknown
	}
}

// QBitClient returns the configured qBittorrent client (password from a 0600 file).
func (r *Runner) QBitClient() (seed.Client, error) { return r.qbitClient() }

func (r *Runner) qbitPassword() (string, error) {
	q := r.Config.QBittorrent
	if q == nil || strings.TrimSpace(q.PasswordFile) == "" {
		return "", errors.New("qbittorrent password file is not configured")
	}
	password, err := readKeyFile(q.PasswordFile)
	if err != nil {
		return "", err
	}
	if r.Logger != nil {
		r.Logger.AddSecret(password)
	}
	return password, nil
}

func (r *Runner) qbitClient() (seed.Client, error) {
	q := r.Config.QBittorrent
	if q == nil {
		return nil, errors.New("qbittorrent is not configured")
	}
	password, err := r.qbitPassword()
	if err != nil {
		return nil, fmt.Errorf("qbittorrent password: %w", err)
	}
	return seed.NewQBit(q.Host, q.Username, password), nil
}

// ConfigProbe is the read-only qBittorrent result of check-config.
type ConfigProbe struct {
	Version  string
	Warnings []string
}

// CheckQBit logs in, reads the WebUI version, checks that the configured
// save path exists on the qBittorrent side, and warns when default automatic
// torrent management is enabled. It does not add or reconfigure torrents.
// A nil web uses the configured client.
func (r *Runner) CheckQBit(ctx context.Context, web seed.WebUI) (ConfigProbe, error) {
	q := r.Config.QBittorrent
	if q == nil {
		return ConfigProbe{}, errors.New("qbittorrent is not configured")
	}
	if _, err := r.qbitPassword(); err != nil {
		return ConfigProbe{}, fmt.Errorf("qbittorrent password: %w", err)
	}
	if web == nil {
		client, err := r.qbitClient()
		if err != nil {
			return ConfigProbe{}, err
		}
		inspected, ok := client.(seed.WebUI)
		if !ok {
			return ConfigProbe{}, errors.New("qbittorrent client cannot be inspected")
		}
		web = inspected
	}
	if err := web.Login(ctx); err != nil {
		return ConfigProbe{}, fmt.Errorf("qbittorrent login: %w", err)
	}
	report, err := web.Inspect(ctx, q.SavePath)
	if err != nil {
		return ConfigProbe{}, fmt.Errorf("qbittorrent inspect: %w", err)
	}
	if !report.SavePathExists {
		return ConfigProbe{}, fmt.Errorf("qbittorrent nie widzi save_path %q (ścieżka z punktu widzenia kontenera qBittorrent)", q.SavePath)
	}
	probe := ConfigProbe{Version: report.Version}
	if report.AutoTMM {
		probe.Warnings = append(
			probe.Warnings,
			"qBittorrent ma włączone domyślne automatyczne zarządzanie torrentem (auto_tmm_enabled). Przy włączonym ATM zmiana kategorii może przenieść pliki. seed wysyła autoTMM=false i jawny save_path.",
		)
	}
	return probe, nil
}

// SeedStatus reports, read only, the qBittorrent state of every accepted upload.
func (r *Runner) SeedStatus(ctx context.Context, client seed.Client) ([]SeedStatusResult, error) {
	if client == nil {
		var err error
		if client, err = r.qbitClient(); err != nil {
			return nil, err
		}
	}
	receipts, err := r.Receipts()
	if err != nil {
		return nil, err
	}
	if err := client.Login(ctx); err != nil {
		return nil, fmt.Errorf("qbittorrent login: %w", err)
	}
	var out []SeedStatusResult
	for _, rc := range receipts {
		if rc.State != ReceiptAccepted {
			continue
		}
		res := SeedStatusResult{Release: rc.Release, Seed: SeedUnknown}
		data, err := tm.ReadFile(rc.RegisteredTorrent)
		if err != nil {
			res.Error = "brak torrenta pobranego z PTT"
			out = append(out, res)
			continue
		}
		desc, err := tm.Describe(data)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.InfoHash = desc.InfoHash
		t, err := client.Find(ctx, desc.InfoHash)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.Seed = classifySeed(t)
		if t != nil {
			res.State, res.Progress = t.State, t.Progress
		}
		out = append(out, res)
	}
	return out, nil
}

// RenderSeedStatus writes the seeding table.
func RenderSeedStatus(w io.Writer, results []SeedStatusResult) {
	if len(results) == 0 {
		_, _ = fmt.Fprintln(w, "brak przyjętych uploadów")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEED\tWYDANIE\tSTAN qBIT\tPOSTĘP\tUWAGI")
	for _, r := range results {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f%%\t%s\n", r.Seed, r.Release, dash(r.State), r.Progress*100, r.Error)
	}
	_ = tw.Flush()
}

// HistoryHeader is the column set of the uploads history CSV.
var HistoryHeader = []string{
	"release", "tracker", "state", "ptt_id", "torrent_url", "data_match", "moderation",
	"moderation_checked_at", "started_at", "finished_at", "fingerprint", "tm_info_hash",
}

// WriteHistory writes every receipt as one CSV row to path (mode 0644: no
// secrets; meant for the Release Panel). The file is replaced atomically.
func (r *Runner) WriteHistory(path string) (int, error) {
	if path == "" {
		path = filepath.Join(r.Config.WorkDir, "history", "zulea-upbrr-history.csv")
	}
	receipts, err := r.Receipts()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("history: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("history: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	w := csv.NewWriter(tmp)
	_ = w.Write(HistoryHeader)
	for _, rc := range receipts {
		match := ""
		if rc.DataMatch != nil {
			match = map[bool]string{true: "TAK", false: "NIE"}[*rc.DataMatch]
		}
		_ = w.Write([]string{
			rc.Release, rc.Tracker, rc.State, rc.TorrentID, rc.TorrentURL, match, rc.Moderation,
			rc.ModerationCheckedAt, rc.StartedAt, rc.FinishedAt, rc.Fingerprint, rc.InfoHash,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("history: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("history: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, fmt.Errorf("history: %w", err)
	}
	return len(receipts), nil
}
