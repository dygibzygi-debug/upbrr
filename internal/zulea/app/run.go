// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/autobrr/upbrr/internal/redaction"
	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/pkg/api"
)

// Outcome is the dry-run result for one release.
type Outcome struct {
	Release string        `json:"release"`
	Tracker string        `json:"tracker"`
	Status  string        `json:"status"` // READY or STOP
	Reason  string        `json:"reason,omitempty"`
	Plan    *pttplan.Plan `json:"plan,omitempty"`
	// Description is the exact description text upbrr would send.
	Description string `json:"description,omitempty"`
	ReportPath  string `json:"report_path,omitempty"`
}

// Statuses.
const (
	StatusReady = "READY"
	StatusStop  = "STOP"
)

// Logger is a secret-aware api.Logger writing to w. Every line is passed
// through upbrr's redaction plus an explicit list of known secret values.
type Logger struct {
	mu      sync.Mutex
	w       io.Writer
	debug   bool
	secrets []string
}

// NewLogger returns a logger; debug enables Debug/Trace lines.
func NewLogger(w io.Writer, debug bool) *Logger { return &Logger{w: w, debug: debug} }

// AddSecret registers a value that must never be printed.
func (l *Logger) AddSecret(secret string) {
	if strings.TrimSpace(secret) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secrets = append(l.secrets, secret)
}

// MaskKnown replaces every registered secret value with <MASKED>. Reports use
// it so evidence digests (SHA-256, infohash) stay readable.
func (l *Logger) MaskKnown(text string) string {
	l.mu.Lock()
	secrets := append([]string(nil), l.secrets...)
	l.mu.Unlock()
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, "<MASKED>")
	}
	return text
}

// Sanitize masks known secrets and applies upbrr's secret-pattern redaction
// (URLs, key=value pairs, long hex tokens). Log lines use it.
func (l *Logger) Sanitize(text string) string {
	l.mu.Lock()
	secrets := append([]string(nil), l.secrets...)
	l.mu.Unlock()
	return redaction.RedactValue(l.MaskKnown(text), nil, secrets...)
}

// Evidencef logs a Zulea line whose values are evidence digests (SHA-256,
// infohash, fingerprint). Only registered secrets are masked, so the digests
// stay readable; upbrr's own lines keep full pattern redaction.
func (l *Logger) Evidencef(format string, args ...any) {
	line := l.MaskKnown(fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.w, "INFO %s\n", line)
}

func (l *Logger) write(level, format string, args ...any) {
	line := l.Sanitize(fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.w, "%s %s\n", level, line)
}

func (l *Logger) Tracef(format string, args ...any) {
	if l.debug {
		l.write("TRACE", format, args...)
	}
}
func (l *Logger) Debugf(format string, args ...any) {
	if l.debug {
		l.write("DEBUG", format, args...)
	}
}
func (l *Logger) Infof(format string, args ...any)  { l.write("INFO", format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.write("WARN", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.write("ERROR", format, args...) }

var _ api.Logger = (*Logger)(nil)

// Runner executes dry-runs for a configuration.
type Runner struct {
	Config Config
	Logger *Logger
	// Movies overrides the TMDB client (tests).
	Movies pttplan.MovieSource
	// MediaInfo overrides the MediaInfo source (tests).
	MediaInfo pttplan.MediaInfoSource
}

// DryRun prepares and plans every release for PTT. A failure of one release
// is a STOP for that release only; the others continue.
func (r *Runner) DryRun(ctx context.Context, releases []string) ([]Outcome, error) {
	if !r.Config.PTTEnabled() {
		return nil, errors.New("no tracker enabled: add \"PTT\" to trackers in the config")
	}
	if len(releases) == 0 {
		return nil, errors.New("no releases given")
	}
	logger := r.Logger
	if logger == nil {
		logger = NewLogger(io.Discard, false)
	}
	movies := r.Movies
	if movies == nil {
		key, err := readKeyFile(r.Config.TMDBKeyFile)
		if err != nil {
			return nil, fmt.Errorf("tmdb: %w", err)
		}
		logger.AddSecret(key)
		client := tmdb.NewClient(key)
		if r.Config.TMDBBaseURL != "" {
			client.BaseURL = r.Config.TMDBBaseURL
		}
		movies = client
	}
	apiKey := ""
	if r.Config.PTT.APIKeyFile != "" {
		key, err := readKeyFile(r.Config.PTT.APIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("ptt: %w", err)
		}
		logger.AddSecret(key)
		apiKey = key
	}
	mediaInfo := r.MediaInfo
	if mediaInfo == nil {
		mediaInfo = pttplan.UpbrrMediaInfo{TempRoot: filepath.Join(r.Config.WorkDir, "mediainfo"), Logger: logger}
	}
	adapter, err := input.Open(input.Sources{
		LibraryRoot:  r.Config.LibraryRoot,
		FreezePath:   r.Config.FreezePath,
		FreezeSHA256: r.Config.FreezeSHA256,
		TitlesPath:   r.Config.TitlesPath,
		ManifestPath: r.Config.ManifestPath,
		TorrentDir:   r.Config.TMOutputDir,
	})
	if err != nil {
		return nil, fmt.Errorf("zulea: open inputs: %w", err)
	}
	logger.Evidencef("zulea: dry-run start trackers=PTT releases=%d freeze_sha256=%s", len(releases), adapter.Freeze().SHA256)
	outcomes := make([]Outcome, 0, len(releases))
	for _, rel := range releases {
		if err := ctx.Err(); err != nil {
			return outcomes, fmt.Errorf("zulea: dry-run canceled: %w", err)
		}
		outcome := Outcome{
			Release: rel,
			Tracker: input.PTT.Tracker,
			Status:  StatusStop,
		}
		prepared, err := adapter.Prepare(rel, input.PTT)
		if err == nil {
			var plan *pttplan.Plan
			plan, err = pttplan.Build(ctx, prepared, pttplan.Options{
				WorkDir:         r.Config.WorkDir,
				APIKey:          apiKey,
				Anonymous:       r.Config.PTT.Anonymous,
				PersonalRelease: r.Config.PTT.PersonalRelease,
				ModQueue:        r.Config.PTT.ModQueue,
				Taxonomy:        r.Config.PTT.Taxonomy,
				Movies:          movies,
				MediaInfo:       mediaInfo,
				Logger:          logger,
			})
			if err == nil {
				outcome.Status = StatusReady
				outcome.Plan = plan
				outcome.Description = plan.Description
			}
		}
		if err != nil {
			outcome.Reason = logger.MaskKnown(err.Error())
			logger.Warnf("zulea: tracker=PTT release=%s decision=stop reason=%s", rel, outcome.Reason)
		} else {
			logger.Evidencef("zulea: tracker=PTT release=%s decision=ready infohash=%s fingerprint=%s", rel, outcome.Plan.InfoHash, outcome.Plan.Fingerprint)
		}
		path, werr := r.writeReport(outcome)
		if werr != nil {
			return outcomes, werr
		}
		outcome.ReportPath = path
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

// writeReport stores the full dry-run (including MediaInfo and description) as
// private JSON in the work directory.
func (r *Runner) writeReport(outcome Outcome) (string, error) {
	dir := filepath.Join(r.Config.WorkDir, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	name := outcome.Release + ".ptt.stop.json"
	if outcome.Plan != nil {
		name = outcome.Release + ".ptt." + outcome.Plan.Fingerprint[:16] + ".json"
	}
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	if r.Logger != nil {
		data = []byte(r.Logger.MaskKnown(string(data)))
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("report: %w", err)
	}
	return path, nil
}

// Render writes the human-readable dry-run preview. Known secrets are masked
// through mask when non-nil.
func Render(w io.Writer, outcomes []Outcome, mask func(string) string) {
	if mask == nil {
		mask = func(s string) string { return s }
	}
	var buf strings.Builder
	render(&buf, outcomes)
	_, _ = io.WriteString(w, mask(buf.String()))
}

func render(w io.Writer, outcomes []Outcome) {
	ready := 0
	for _, o := range outcomes {
		fmt.Fprintf(w, "\n=== %s — %s — %s\n", o.Tracker, o.Release, o.Status)
		if o.Status != StatusReady {
			fmt.Fprintf(w, "POWÓD: %s\n", o.Reason)
			if o.ReportPath != "" {
				fmt.Fprintf(w, "raport: %s\n", o.ReportPath)
			}
			continue
		}
		ready++
		p := o.Plan
		fmt.Fprintf(w, "endpoint: %s\n", p.Endpoint)
		fmt.Fprintf(w, "tytuł:    %s\n", p.Title)
		keys := make([]string, 0, len(p.Payload))
		for k := range p.Payload {
			if k != "description" && k != "mediainfo" && k != "name" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprintln(w, "pola formularza:")
		for _, k := range keys {
			fmt.Fprintf(w, "  %-17s %s\n", k, p.Payload[k])
		}
		fmt.Fprintf(w, "  %-17s %d znaków (pełny tekst w raporcie)\n", "mediainfo", len(p.Payload["mediainfo"]))
		fmt.Fprintln(w, "pliki:")
		for _, f := range p.Files {
			fmt.Fprintf(w, "  %-8s %s (%d B, sha256 %s)\n", f.Field, f.Path, f.Size, f.SHA256)
		}
		fmt.Fprintf(w, "infohash: %s (TM: %s)\n", p.InfoHash, p.TMInfoHash)
		fmt.Fprintf(w, "ID PTT potwierdzone: %t\n", p.TaxonomyConfirmed)
		for _, warning := range p.Warnings {
			fmt.Fprintf(w, "UWAGA: %s\n", warning)
		}
		fmt.Fprintln(w, "opis (dokładnie to, co zostałoby wysłane):")
		fmt.Fprintln(w, "-----8<-----")
		fmt.Fprintln(w, o.Description)
		fmt.Fprintln(w, "-----8<-----")
		fmt.Fprintf(w, "fingerprint: %s\nraport: %s\n", p.Fingerprint, o.ReportPath)
	}
	fmt.Fprintf(w, "\nPODSUMOWANIE: READY=%d STOP=%d. Tryb wyłącznie dry-run: nic nie zostało wysłane.\n", ready, len(outcomes)-ready)
}
