// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package pttplan builds a PTT dry-run from verified Zulea inputs using the
// upbrr PTT (Unit3D) tracker definition, then checks that the payload upbrr
// would send is exactly what the Zulea chain proved.
//
// Stage 1 is dry-run only: the plan is built with PreparationIntentDryRun,
// which never carries a submit function, and this package exposes no upload.
package pttplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/autobrr/upbrr/internal/config"
	"github.com/autobrr/upbrr/internal/pathing"
	"github.com/autobrr/upbrr/internal/trackers"
	"github.com/autobrr/upbrr/internal/trackers/impl"
	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/tm"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/pkg/api"
)

// ErrPlan marks a plan that must not be offered for upload (STOP).
var ErrPlan = errors.New("pttplan: plan rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPlan, fmt.Sprintf(format, args...))
}

// MovieSource returns TMDB facts for a frozen TMDB_ID (decision D-2).
type MovieSource interface {
	Movie(ctx context.Context, frozenID string) (tmdb.Movie, error)
}

// MediaInfoSource renders the MediaInfo text for the MKV into a private file
// outside the library and returns its path.
type MediaInfoSource interface {
	MediaInfoText(ctx context.Context, mkvPath string, release string) (string, error)
}

// Taxonomy holds PTT form IDs confirmed by the owner (decision D-4b). When
// set, the upbrr payload must use exactly these IDs or the plan stops.
type Taxonomy struct {
	Category   map[string]string `json:"category,omitempty"`
	Type       map[string]string `json:"type,omitempty"`
	Resolution map[string]string `json:"resolution,omitempty"`
}

// Options configure a plan.
type Options struct {
	// WorkDir holds generated artifacts (upload copy, MediaInfo); never the library.
	WorkDir string
	// APIKey is the PTT API key. Dry-run never transmits it; when empty a
	// placeholder satisfies upbrr's preflight and a warning is recorded.
	APIKey          string
	Anonymous       bool
	PersonalRelease bool
	ModQueue        bool
	Taxonomy        *Taxonomy
	Movies          MovieSource
	MediaInfo       MediaInfoSource
	Logger          api.Logger
}

// File is one multipart file of the plan.
type File struct {
	Field  string `json:"field"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Plan is the verified PTT dry-run.
type Plan struct {
	Release     string            `json:"release"`
	Tracker     string            `json:"tracker"`
	Endpoint    string            `json:"endpoint"`
	Status      string            `json:"status"`
	Message     string            `json:"message"`
	Title       string            `json:"title"`
	Payload     map[string]string `json:"payload"`
	Description string            `json:"-"`
	Files       []File            `json:"files"`
	InfoHash    string            `json:"info_hash"`
	TMInfoHash  string            `json:"tm_info_hash"`
	// TaxonomyConfirmed reports whether owner-confirmed PTT IDs were checked.
	TaxonomyConfirmed bool     `json:"taxonomy_confirmed"`
	Warnings          []string `json:"warnings"`
	// Fingerprint is SHA-256 over the canonical payload and file digests: the
	// exact content a later confirmed upload must reproduce.
	Fingerprint string `json:"fingerprint"`
}

// placeholderAPIKey is used only for dry-run when no key is configured.
const placeholderAPIKey = "dry-run-no-key" //nolint:gosec // Not a credential: fixed marker that dry-run never transmits.

// Build prepares the PTT dry-run for prepared.
func Build(ctx context.Context, prepared *input.Prepared, opts Options) (*Plan, error) {
	if prepared == nil || prepared.Profile.Tracker != input.PTT.Tracker {
		return nil, reject("prepared inputs are not for PTT")
	}
	if strings.TrimSpace(opts.WorkDir) == "" || opts.Movies == nil || opts.MediaInfo == nil {
		return nil, reject("work directory, movie source and MediaInfo source are required")
	}
	if insideLibrary(prepared.Directory, opts.WorkDir) {
		return nil, reject("work directory must be outside the release library")
	}
	logger := opts.Logger
	if logger == nil {
		logger = api.NopLogger{}
	}
	var warnings []string
	warnings = append(warnings, prepared.Warnings...)

	movie, err := opts.Movies.Movie(ctx, prepared.Record.TMDBID)
	if err != nil {
		return nil, reject("TMDB: %v", err)
	}
	if strconv.Itoa(movie.ID) != strings.TrimLeft(prepared.Record.TMDBID, "0") {
		return nil, reject("TMDB id %d differs from freeze %s", movie.ID, prepared.Record.TMDBID)
	}
	if movie.IMDbID == 0 {
		warnings = append(warnings, "TMDB nie zwrócił IMDb ID — pole imdb = 0")
	}

	uploadPath := filepath.Join(opts.WorkDir, "upload", prepared.Torrent.InfoHash, prepared.Release+".ptt.torrent")
	if err := prepared.Torrent.WriteUploadCopy(uploadPath); err != nil {
		return nil, reject("upload torrent copy: %v", err)
	}
	mediaInfoPath, err := opts.MediaInfo.MediaInfoText(ctx, prepared.MKV.Path, prepared.Release)
	if err != nil {
		return nil, reject("MediaInfo: %v", err)
	}
	if insideLibrary(prepared.Directory, mediaInfoPath) {
		return nil, reject("MediaInfo artifact must not be written into the library")
	}

	registry, err := impl.NewRegistry()
	if err != nil {
		return nil, reject("upbrr registry: %v", err)
	}
	definition, ok := registry.Lookup(input.PTT.Tracker)
	if !ok {
		return nil, reject("upbrr registry has no PTT definition")
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		apiKey = placeholderAPIKey
		warnings = append(warnings, "brak klucza API PTT — podgląd zbudowany bez klucza (dry-run nie wysyła klucza)")
	}
	title := prepared.Title
	tmdbID := movie.ID
	subject := api.UploadSubject{
		SourcePath:        prepared.Directory,
		ReleaseName:       prepared.Release,
		ReleaseNameNoTag:  strings.TrimSuffix(prepared.Release, "-Zulea"),
		TorrentPath:       uploadPath,
		MediaInfoTextPath: mediaInfoPath,
		SceneNFOPath:      prepared.NFO.Path,
		Type:              "REMUX",
		Tag:               "Zulea",
		PersonalRelease:   opts.PersonalRelease,
		Assessments:       api.ReleaseAssessments{MediaInfoEncodeSettings: api.EncodeSettingsStatusNotApplicable},
		Identity: api.ExternalIdentity{
			Category: api.CanonicalCategoryMovie,
			TMDBID:   tmdbID,
			IMDBID:   movie.IMDbID,
		},
		Release: api.ReleaseInfo{
			Category:   "MOVIE",
			Resolution: prepared.Parsed.Res,
			Source:     "BluRay",
			Type:       "REMUX",
		},
		ProviderMetadata: api.SourceScopedMetadata{
			SourcePath: prepared.Directory,
			TMDB:       &api.TMDBMetadata{Keywords: movie.Keywords},
		},
	}
	runtime := trackers.PreparationRuntime{DBPath: filepath.Join(opts.WorkDir, "upbrr.db")}
	plan, failure := definition.Prepare(ctx, trackers.PreparationInput{
		Intent:              trackers.PreparationIntentDryRun,
		Tracker:             input.PTT.Tracker,
		Meta:                subject,
		RequestedUploadName: &title,
		TrackerConfig: config.TrackerConfig{
			APIKey: apiKey,
			Anon:   opts.Anonymous,
			ModQ:   opts.ModQueue,
		},
		Runtime: runtime,
		Logger:  logger,
		Assets:  &trackers.DescriptionAssets{Description: prepared.Description, Final: true},
	})
	if failure != nil {
		return nil, reject("upbrr PTT preparation: %s", failure.Error())
	}
	defer func() { _ = plan.Release() }()
	entry := plan.DryRun()
	out := &Plan{
		Release:    prepared.Release,
		Tracker:    entry.Tracker,
		Endpoint:   entry.Endpoint,
		Status:     entry.Status,
		Message:    entry.Message,
		Title:      entry.Payload["name"],
		Payload:    entry.Payload,
		TMInfoHash: prepared.Manifest.InfoHash,
	}
	if out.Description, err = verifyEntry(entry, prepared, title, uploadPath, tmdbID); err != nil {
		return nil, err
	}
	if out.Files, err = fileEvidence(entry.Files); err != nil {
		return nil, err
	}
	hash, err := infoHashOfFile(uploadPath)
	if err != nil {
		return nil, err
	}
	if hash != prepared.Torrent.InfoHash || hash != prepared.Manifest.InfoHash {
		return nil, reject("upload torrent infohash %s differs from TM %s", hash, prepared.Manifest.InfoHash)
	}
	out.InfoHash = hash
	if opts.Taxonomy != nil {
		if err := checkTaxonomy(*opts.Taxonomy, entry.Payload, prepared); err != nil {
			return nil, err
		}
		out.TaxonomyConfirmed = true
	} else {
		warnings = append(warnings, "ID kategorii/typu/rozdzielczości PTT niepotwierdzone (D-4b) — użyto domyślnych ID Unit3D z upbrr")
	}
	if entry.Banned {
		return nil, reject("release group is banned on PTT: %s", entry.BannedReason)
	}
	out.Warnings = warnings
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	out.Fingerprint, err = fingerprint(out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// verifyEntry enforces that upbrr kept every Zulea input intact.
func verifyEntry(entry api.TrackerDryRunEntry, prepared *input.Prepared, title, uploadPath string, tmdbID int) (string, error) {
	if entry.Status != "ready" {
		return "", reject("upbrr dry-run status %q: %s", entry.Status, entry.Message)
	}
	if got := entry.Payload["name"]; got != title {
		return "", reject("payload name %q differs from the names.py title %q", got, title)
	}
	// upbrr trims surrounding whitespace of a final description; nothing else may change.
	wantDescription := strings.TrimSpace(prepared.Description)
	if entry.Payload["description"] != wantDescription || entry.Description != wantDescription {
		return "", reject("payload description differs from the ready description file")
	}
	if entry.Payload["tmdb"] != strconv.Itoa(tmdbID) {
		return "", reject("payload tmdb %q differs from freeze", entry.Payload["tmdb"])
	}
	if strings.TrimSpace(entry.Payload["mediainfo"]) == "" {
		return "", reject("payload MediaInfo is empty")
	}
	if entry.Payload["bdinfo"] != "" {
		return "", reject("payload BDInfo must be empty for an MKV remux")
	}
	var torrentPath, nfoPath string
	for _, f := range entry.Files {
		switch f.Field {
		case "torrent":
			torrentPath = f.Path
		case "nfo":
			nfoPath = f.Path
		default:
			return "", reject("unexpected payload file field %q", f.Field)
		}
	}
	if torrentPath != uploadPath {
		return "", reject("payload torrent is not the verified TM upload copy")
	}
	if nfoPath != prepared.NFO.Path {
		return "", reject("payload NFO is not the library NFO")
	}
	nfo, err := os.ReadFile(nfoPath)
	if err != nil {
		return "", reject("read NFO: %v", err)
	}
	sum := sha256.Sum256(nfo)
	if hex.EncodeToString(sum[:]) != prepared.Manifest.NFOSHA256 {
		return "", reject("payload NFO bytes differ from the TM manifest")
	}
	return wantDescription, nil
}

func fileEvidence(files []api.TrackerDryRunFile) ([]File, error) {
	out := make([]File, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, reject("read payload file %s: %v", f.Field, err)
		}
		sum := sha256.Sum256(data)
		out = append(out, File{
			Field:  f.Field,
			Path:   f.Path,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	slices.SortFunc(out, func(a, b File) int { return strings.Compare(a.Field, b.Field) })
	return out, nil
}

func infoHashOfFile(path string) (string, error) {
	data, err := tm.ReadFile(path)
	if err != nil {
		return "", reject("%v", err)
	}
	hash, err := tm.InfoHashOf(data)
	if err != nil {
		return "", reject("%v", err)
	}
	return hash, nil
}

func checkTaxonomy(tax Taxonomy, payload map[string]string, prepared *input.Prepared) error {
	checks := []struct {
		field, key string
		table      map[string]string
	}{
		{"category_id", "MOVIE", tax.Category},
		{"type_id", "REMUX", tax.Type},
		{"resolution_id", prepared.Parsed.Res, tax.Resolution},
	}
	for _, c := range checks {
		want, ok := c.table[c.key]
		if !ok || strings.TrimSpace(want) == "" {
			return reject("confirmed PTT %s for %s is missing from the taxonomy", c.field, c.key)
		}
		if payload[c.field] != want {
			return reject("payload %s=%s differs from confirmed PTT ID %s for %s", c.field, payload[c.field], want, c.key)
		}
	}
	return nil
}

func fingerprint(p *Plan) (string, error) {
	keys := make([]string, 0, len(p.Payload))
	for k := range p.Payload {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fields := make([][2]string, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, [2]string{k, p.Payload[k]})
	}
	files := make([][3]string, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, [3]string{f.Field, strconv.FormatInt(f.Size, 10), f.SHA256})
	}
	data, err := json.Marshal(struct {
		Tracker  string      `json:"tracker"`
		Endpoint string      `json:"endpoint"`
		Fields   [][2]string `json:"fields"`
		Files    [][3]string `json:"files"`
	}{p.Tracker, p.Endpoint, fields, files})
	if err != nil {
		return "", fmt.Errorf("pttplan: fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// insideLibrary reports whether path lies inside the library root that holds releaseDir.
func insideLibrary(releaseDir, path string) bool {
	return pathing.IsWithinRoot(filepath.Dir(releaseDir), path)
}
