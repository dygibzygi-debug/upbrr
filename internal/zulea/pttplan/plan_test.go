// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/input"
	"github.com/autobrr/upbrr/internal/zulea/tm"
	"github.com/autobrr/upbrr/internal/zulea/tmdb"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const apiKeySentinel = "PTTAPIKEYSENTINEL0123456789"

type fakeMovies struct {
	movie tmdb.Movie
	err   error
	calls []string
}

func (f *fakeMovies) Movie(_ context.Context, id string) (tmdb.Movie, error) {
	f.calls = append(f.calls, id)
	return f.movie, f.err
}

type fakeMediaInfo struct{ dir string }

func (f fakeMediaInfo) MediaInfoText(_ context.Context, mkvPath string, release string) (string, error) {
	path := filepath.Join(f.dir, "mediainfo", release+".txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("fake mediainfo dir: %w", err)
	}
	text := "General\nComplete name                            : " + filepath.Base(mkvPath) + "\nFormat                                   : Matroska\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("fake mediainfo write: %w", err)
	}
	return path, nil
}

// captureLogger records every log line so tests can assert secrets never appear.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}
func (l *captureLogger) Tracef(f string, a ...any) { l.add(f, a...) }
func (l *captureLogger) Debugf(f string, a ...any) { l.add(f, a...) }
func (l *captureLogger) Infof(f string, a ...any)  { l.add(f, a...) }
func (l *captureLogger) Warnf(f string, a ...any)  { l.add(f, a...) }
func (l *captureLogger) Errorf(f string, a ...any) { l.add(f, a...) }

func prepared(t *testing.T, env *zuleatest.Env) *input.Prepared {
	t.Helper()
	adapter, err := input.Open(input.Sources{
		LibraryRoot:  env.LibraryRoot,
		FreezePath:   env.FreezePath,
		FreezeSHA256: env.FreezeSHA256,
		TitlesPath:   env.TitlesPath,
		ManifestPath: env.ManifestPath,
		TorrentDir:   env.TorrentDir,
		Disk:         env.DiskCheck(),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := adapter.Prepare(zuleatest.Release, input.PTT)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return p
}

func options(t *testing.T, env *zuleatest.Env, logger *captureLogger) (Options, *fakeMovies) {
	t.Helper()
	movies := &fakeMovies{movie: tmdb.Movie{
		ID:       100001,
		IMDbID:   456,
		Keywords: "music, example",
	}}
	work := filepath.Join(env.Root, "work")
	return Options{
		WorkDir:   work,
		APIKey:    apiKeySentinel,
		Movies:    movies,
		MediaInfo: fakeMediaInfo{dir: work},
		Logger:    logger,
	}, movies
}

func TestBuildPTTDryRunThroughUpbrr(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	logger := &captureLogger{}
	opts, movies := options(t, env, logger)
	p := prepared(t, env)
	plan, err := Build(context.Background(), p, opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if plan.Status != "ready" || plan.Tracker != "PTT" || plan.Endpoint != "https://polishtorrent.top/api/torrents/upload" {
		t.Fatalf("plan %#v", plan)
	}
	// Mandatory: name = names.py result.
	if plan.Title != p.Title || plan.Payload["name"] != p.Title {
		t.Fatalf("title %q payload name %q want %q", plan.Title, plan.Payload["name"], p.Title)
	}
	// Mandatory: sent description = input file (upbrr trims surrounding whitespace only).
	file, err := os.ReadFile(filepath.Join(env.ReleaseDir, zuleatest.Release+".cinemamovies.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Payload["description"] != strings.TrimSpace(string(file)) {
		t.Fatalf("description changed:\n%q\nvs\n%q", plan.Payload["description"], string(file))
	}
	// Mandatory: TM torrent accepted with unchanged info (same infohash), no source, no announce.
	if plan.InfoHash != env.InfoHash || plan.TMInfoHash != env.InfoHash {
		t.Fatalf("infohash %s want %s", plan.InfoHash, env.InfoHash)
	}
	var torrentFile, nfoFile File
	for _, f := range plan.Files {
		switch f.Field {
		case "torrent":
			torrentFile = f
		case "nfo":
			nfoFile = f
		}
	}
	upload, err := os.ReadFile(torrentFile.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(upload, []byte("announce")) || bytes.Contains(upload, []byte(zuleatest.PasskeySentinel)) ||
		bytes.Contains(upload, []byte("6:source")) {
		t.Fatal("upload torrent must have no announce, passkey or source")
	}
	tmOriginal, err := tm.ReadFile(env.TorrentPath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := tm.Parse(tmOriginal, tm.Expectation{Release: zuleatest.Release, Sizes: map[string]int64{
		zuleatest.Release + ".mkv": int64(len(env.MKVBytes)), zuleatest.Release + ".nfo": int64(len(env.NFOBytes)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(upload, original.InfoBytes()) {
		t.Fatal("upload torrent must carry the TM info bytes verbatim")
	}
	if nfoFile.Path != env.NFOPath || nfoFile.Size != int64(len(env.NFOBytes)) {
		t.Fatalf("nfo file %#v", nfoFile)
	}
	if plan.Payload["tmdb"] != zuleatest.TMDBID || plan.Payload["imdb"] != "0000456" || plan.Payload["keywords"] != "music, example" {
		t.Fatalf("identity fields %q %q %q", plan.Payload["tmdb"], plan.Payload["imdb"], plan.Payload["keywords"])
	}
	if plan.Payload["category_id"] != "1" || plan.Payload["type_id"] != "2" || plan.Payload["resolution_id"] != "3" {
		t.Fatalf("unit3d ids %q %q %q", plan.Payload["category_id"], plan.Payload["type_id"], plan.Payload["resolution_id"])
	}
	if plan.TaxonomyConfirmed {
		t.Fatal("taxonomy must be reported unconfirmed without owner IDs")
	}
	if len(movies.calls) != 1 || movies.calls[0] != zuleatest.TMDBID {
		t.Fatalf("TMDB must be queried exactly once by the frozen id, got %q", movies.calls)
	}
	if len(plan.Fingerprint) != 64 {
		t.Fatalf("fingerprint %q", plan.Fingerprint)
	}
	// Library untouched: the release directory holds only the original files.
	entries, err := os.ReadDir(env.ReleaseDir)
	if err != nil || len(entries) != 5 {
		t.Fatalf("library directory changed: %d entries err=%v", len(entries), err)
	}
	// Mandatory: secrets never reach logs or the serialized plan.
	serialized, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{apiKeySentinel, zuleatest.PasskeySentinel, zuleatest.DeleteSentinel} {
		if bytes.Contains(serialized, []byte(secret)) {
			t.Fatalf("plan leaks %s", secret)
		}
		for _, line := range logger.lines {
			if strings.Contains(line, secret) {
				t.Fatalf("log leaks secret: %s", line)
			}
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	opts, _ := options(t, env, &captureLogger{})
	first, err := Build(context.Background(), prepared(t, env), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), prepared(t, env), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("identical inputs must give the same fingerprint")
	}
}

func TestBuildStops(t *testing.T) {
	cases := map[string]func(*Options, *fakeMovies){
		"tmdb mismatch": func(_ *Options, m *fakeMovies) { m.movie.ID = 100002 },
		"tmdb error":    func(_ *Options, m *fakeMovies) { m.err = tmdb.ErrMismatch },
		"taxonomy differs": func(o *Options, _ *fakeMovies) {
			o.Taxonomy = &Taxonomy{
				Category:   map[string]string{"MOVIE": "1"},
				Type:       map[string]string{"REMUX": "7"},
				Resolution: map[string]string{"1080p": "3"},
			}
		},
		"taxonomy incomplete": func(o *Options, _ *fakeMovies) {
			o.Taxonomy = &Taxonomy{Category: map[string]string{"MOVIE": "1"}}
		},
		"work dir in library": func(_ *Options, _ *fakeMovies) {},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := zuleatest.New(t, zuleatest.Options{})
			opts, movies := options(t, env, &captureLogger{})
			mutate(&opts, movies)
			if name == "work dir in library" {
				opts.WorkDir = filepath.Join(env.LibraryRoot, "work")
			}
			_, err := Build(context.Background(), prepared(t, env), opts)
			if !errors.Is(err, ErrPlan) {
				t.Fatalf("expected ErrPlan, got %v", err)
			}
			t.Logf("stop reason: %v", err)
		})
	}
}

func TestBuildAcceptsConfirmedTaxonomy(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	opts, _ := options(t, env, &captureLogger{})
	opts.Taxonomy = &Taxonomy{
		Category:   map[string]string{"MOVIE": "1"},
		Type:       map[string]string{"REMUX": "2"},
		Resolution: map[string]string{"1080p": "3"},
	}
	plan, err := Build(context.Background(), prepared(t, env), opts)
	if err != nil || !plan.TaxonomyConfirmed {
		t.Fatalf("plan %#v err=%v", plan, err)
	}
}
