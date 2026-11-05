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
	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
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
	// Dupe is the PTT duplicate-check result (nil when the plan stopped earlier).
	Dupe *pttdupe.Result `json:"dupe,omitempty"`
	// Canary lists what still blocks a first real upload; empty means ready.
	Canary CanaryGate `json:"canary"`
	// Description is the exact description text upbrr would send.
	Description string `json:"description,omitempty"`
	ReportPath  string `json:"report_path,omitempty"`
}

// CanaryGate reports readiness for a first, human-confirmed upload (stage 3).
// It never authorizes an upload by itself.
type CanaryGate struct {
	Ready    bool     `json:"ready"`
	Blockers []string `json:"blockers"`
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
	// SkipDupe disables the duplicate check (tests of unrelated behavior).
	SkipDupe bool
}

// movieCache performs at most one TMDB request per frozen id per run.
type movieCache struct {
	source pttplan.MovieSource
	mu     sync.Mutex
	movies map[string]tmdb.Movie
}

func (c *movieCache) Movie(ctx context.Context, id string) (tmdb.Movie, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if movie, ok := c.movies[id]; ok {
		return movie, nil
	}
	movie, err := c.source.Movie(ctx, id)
	if err != nil {
		return tmdb.Movie{}, fmt.Errorf("tmdb movie %s: %w", id, err)
	}
	c.movies[id] = movie
	return movie, nil
}

func (r *Runner) mediaInfoSource(logger *Logger) pttplan.MediaInfoSource {
	if r.MediaInfo != nil {
		return r.MediaInfo
	}
	switch r.Config.MediaInfo.Source {
	case MediaInfoCLI:
		return pttplan.CLIMediaInfo{Binary: r.Config.MediaInfo.Binary, OutputDir: filepath.Join(r.Config.WorkDir, "mediainfo")}
	case MediaInfoFile:
		return pttplan.FileMediaInfo{Dir: r.Config.MediaInfo.Dir}
	default:
		return pttplan.UpbrrMediaInfo{TempRoot: filepath.Join(r.Config.WorkDir, "mediainfo"), Logger: logger}
	}
}

// canaryGate lists every unmet stage-3 precondition from the review (W-1, D-5, D-6).
func (r *Runner) canaryGate(plan *pttplan.Plan, dupeResult *pttdupe.Result, apiKey string) CanaryGate {
	var blockers []string
	if plan == nil {
		blockers = append(blockers, "plan zatrzymany (STOP)")
	} else if !plan.TaxonomyConfirmed {
		blockers = append(blockers, "W-1: ID kategorii/rodzaju/rozdzielczości PTT niepotwierdzone (ptt.taxonomy)")
	}
	if apiKey == "" {
		blockers = append(blockers, "brak klucza API PTT (ptt.api_key_file)")
	}
	if waiting, err := r.awaitingApproval(); err != nil {
		blockers = append(blockers, "pokwitowania nieczytelne: "+err.Error())
	} else if len(waiting) > 0 {
		blockers = append(blockers, "poprzedni upload niezatwierdzony na PTT (ptt-status): "+strings.Join(waiting, ", "))
	}
	switch {
	case dupeResult == nil:
		blockers = append(blockers, "duplikaty niesprawdzone")
	case dupeResult.Status != pttdupe.StatusClear:
		blockers = append(blockers, "duplikaty: "+dupeResult.Status+" — "+dupeResult.Message)
	}
	if r.Config.PTT.PersonalRelease == nil {
		blockers = append(blockers, "D-5: ptt.personal_release nierozstrzygnięte (sprawdź regulamin PTT)")
	}
	if !r.Config.PTT.ModQueue {
		blockers = append(blockers, "D-5: canary wymaga ptt.mod_queue=true")
	}
	if r.MediaInfo == nil && r.Config.MediaInfo.Source == MediaInfoUpbrr {
		blockers = append(blockers, "D-6: canary wymaga MediaInfo z CLI (mediainfo.source=cli lub file)")
	}
	if blockers == nil {
		blockers = []string{}
	}
	return CanaryGate{Ready: len(blockers) == 0, Blockers: blockers}
}

// session holds everything opened once per run: inputs, TMDB, keys, MediaInfo.
type session struct {
	logger    *Logger
	adapter   *input.Adapter
	movies    pttplan.MovieSource
	apiKey    string
	mediaInfo pttplan.MediaInfoSource
}

func (r *Runner) open(releases []string) (*session, error) {
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
	sess := &session{logger: logger, movies: &movieCache{source: movies, movies: map[string]tmdb.Movie{}}}
	if r.Config.PTT.APIKeyFile != "" {
		key, err := readKeyFile(r.Config.PTT.APIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("ptt: %w", err)
		}
		logger.AddSecret(key)
		sess.apiKey = key
	}
	sess.mediaInfo = r.mediaInfoSource(logger)
	adapter, err := input.Open(input.Sources{
		LibraryRoot:  r.Config.LibraryRoot,
		FreezePath:   r.Config.FreezePath,
		FreezeSHA256: r.Config.FreezeSHA256,
		TitlesPath:   r.Config.TitlesPath,
		ManifestPath: r.Config.ManifestPath,
		TorrentDir:   r.Config.TMOutputDir,
		TMRunsDir:    r.Config.TMRunsDir,
		TMOutputBase: r.Config.TMOutputBase,
		Disk:         r.Config.DiskCheck(),
	})
	if err != nil {
		return nil, fmt.Errorf("zulea: open inputs: %w", err)
	}
	sess.adapter = adapter
	return sess, nil
}

func (r *Runner) planOptions(sess *session) pttplan.Options {
	return pttplan.Options{
		WorkDir:         r.Config.WorkDir,
		APIKey:          sess.apiKey,
		Anonymous:       r.Config.PTT.Anonymous,
		PersonalRelease: r.Config.PTT.PersonalRelease != nil && *r.Config.PTT.PersonalRelease,
		ModQueue:        r.Config.PTT.ModQueue,
		Taxonomy:        r.Config.PTT.Taxonomy,
		Movies:          sess.movies,
		MediaInfo:       sess.mediaInfo,
		Logger:          sess.logger,
	}
}

// evaluate runs the full dry-run for one release: inputs, plan, duplicates, gate.
func (r *Runner) evaluate(ctx context.Context, sess *session, rel string) (Outcome, *input.Prepared) {
	outcome := Outcome{
		Release: rel,
		Tracker: input.PTT.Tracker,
		Status:  StatusStop,
	}
	prepared, err := sess.adapter.Prepare(rel, input.PTT)
	if err == nil {
		var plan *pttplan.Plan
		plan, err = pttplan.Build(ctx, prepared, r.planOptions(sess))
		if err == nil {
			outcome.Status = StatusReady
			outcome.Plan = plan
			outcome.Description = plan.Description
			if !r.SkipDupe {
				err = r.checkDupes(ctx, sess, prepared, &outcome)
				if err != nil {
					outcome.Status = StatusStop
				}
			}
		}
	}
	if outcome.Status == StatusReady {
		outcome.Canary = r.canaryGate(outcome.Plan, outcome.Dupe, sess.apiKey)
	} else {
		outcome.Canary = r.canaryGate(nil, outcome.Dupe, sess.apiKey)
	}
	if err != nil {
		outcome.Reason = sess.logger.MaskKnown(err.Error())
		sess.logger.Warnf("zulea: tracker=PTT release=%s decision=stop reason=%s", rel, outcome.Reason)
	} else {
		sess.logger.Evidencef("zulea: tracker=PTT release=%s decision=ready infohash=%s fingerprint=%s canary_ready=%t",
			rel, outcome.Plan.InfoHash, outcome.Plan.Fingerprint, outcome.Canary.Ready)
	}
	return outcome, prepared
}

func (r *Runner) checkDupes(ctx context.Context, sess *session, prepared *input.Prepared, outcome *Outcome) error {
	movie, err := sess.movies.Movie(ctx, prepared.Record.TMDBID)
	if err != nil {
		return fmt.Errorf("zulea: TMDB for duplicate check: %w", err)
	}
	result, err := pttdupe.Check(ctx, prepared, movie, pttdupe.Options{
		APIKey:  sess.apiKey,
		WorkDir: r.Config.WorkDir,
		Logger:  sess.logger,
	})
	if err != nil {
		return fmt.Errorf("zulea: duplicate check: %w", err)
	}
	outcome.Dupe = &result
	if result.Status == pttdupe.StatusDuplicate {
		return errors.New("zulea: STOP: duplicate on PTT: " + result.Message)
	}
	return nil
}

// DryRun prepares and plans every release for PTT. A failure of one release
// is a STOP for that release only; the others continue.
func (r *Runner) DryRun(ctx context.Context, releases []string) ([]Outcome, error) {
	sess, err := r.open(releases)
	if err != nil {
		return nil, err
	}
	sess.logger.Evidencef("zulea: dry-run start trackers=PTT releases=%d freeze_sha256=%s", len(releases), sess.adapter.Freeze().SHA256)
	outcomes := make([]Outcome, 0, len(releases))
	for _, rel := range releases {
		if err := ctx.Err(); err != nil {
			return outcomes, fmt.Errorf("zulea: dry-run canceled: %w", err)
		}
		outcome, _ := r.evaluate(ctx, sess, rel)
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
			if o.Dupe != nil {
				renderDupe(w, o.Dupe)
			}
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
		renderDupe(w, o.Dupe)
		for _, warning := range p.Warnings {
			fmt.Fprintf(w, "UWAGA: %s\n", warning)
		}
		fmt.Fprintln(w, "opis (dokładnie to, co zostałoby wysłane):")
		fmt.Fprintln(w, "-----8<-----")
		fmt.Fprintln(w, o.Description)
		fmt.Fprintln(w, "-----8<-----")
		fmt.Fprintf(w, "fingerprint: %s\nraport: %s\n", p.Fingerprint, o.ReportPath)
		renderCanary(w, o.Canary)
	}
	fmt.Fprintf(w, "\nPODSUMOWANIE: READY=%d STOP=%d. Tryb wyłącznie dry-run: nic nie zostało wysłane.\n", ready, len(outcomes)-ready)
}

func renderDupe(w io.Writer, d *pttdupe.Result) {
	if d == nil {
		fmt.Fprintln(w, "duplikaty PTT: nie sprawdzano")
		return
	}
	fmt.Fprintf(w, "duplikaty PTT: %s — %s (stron: %d, kandydatów: %d, wyszukiwanie kompletne: %t)\n",
		d.Status, d.Message, d.Pages, d.CandidateCount, d.SearchComplete)
	for _, c := range d.Candidates {
		fmt.Fprintf(w, "  [%s] %s (%s %s) %s\n", c.Relation, c.Name, c.Type, c.Resolution, c.Link)
	}
	for _, reason := range d.ReviewReasons {
		fmt.Fprintf(w, "  do oceny: %s\n", reason)
	}
}

func renderCanary(w io.Writer, gate CanaryGate) {
	if gate.Ready {
		fmt.Fprintln(w, "gotowość do canary: TAK (upload nadal wymaga osobnego potwierdzenia Andryou)")
		return
	}
	fmt.Fprintln(w, "gotowość do canary: NIE")
	for _, b := range gate.Blockers {
		fmt.Fprintf(w, "  bloker: %s\n", b)
	}
}
