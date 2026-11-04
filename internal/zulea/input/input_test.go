// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package input

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/names"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

type recordingReader struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingReader) ReadFile(path string, limit int64) ([]byte, error) {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()
	return osReader{}.ReadFile(path, limit)
}

func sources(env *zuleatest.Env) Sources {
	return Sources{
		LibraryRoot:  env.LibraryRoot,
		FreezePath:   env.FreezePath,
		FreezeSHA256: env.FreezeSHA256,
		TitlesPath:   env.TitlesPath,
		ManifestPath: env.ManifestPath,
		TorrentDir:   env.TorrentDir,
		Disk:         env.DiskCheck(),
	}
}

func prepare(t *testing.T, env *zuleatest.Env) (*Prepared, *recordingReader, error) {
	t.Helper()
	reader := &recordingReader{}
	adapter, err := OpenWithReader(sources(env), reader)
	if err != nil {
		t.Fatalf("open adapter: %v", err)
	}
	prepared, err := adapter.Prepare(zuleatest.Release, PTT)
	return prepared, reader, err
}

func TestPrepareCollectsVerifiedInputs(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	prepared, reader, err := prepare(t, env)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Title != "Przykładowy film / Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-Zulea" {
		t.Fatalf("title %q", prepared.Title)
	}
	if prepared.Title != names.Build(zuleatest.Release, &names.TitleRecord{
		OriginalLanguage: "en",
		TitlePL:          "Przykładowy film",
		TitleOrig:        "Example Film",
		TitleEN:          "Example Film",
	}).PTT {
		t.Fatal("title must equal the names.Build PTT result")
	}
	if prepared.Description != zuleatest.Description || prepared.Torrent.InfoHash != env.InfoHash {
		t.Fatalf("unexpected prepared inputs: %#v", prepared)
	}
	if len(prepared.ImageURLs) != 3 || prepared.ImageURLs[0] != "https://i.ibb.co/aaaa/shot1.png" {
		t.Fatalf("image URLs %q", prepared.ImageURLs)
	}
	if prepared.NFO.Size != int64(len(env.NFOBytes)) || prepared.Record.TMDBID != zuleatest.TMDBID {
		t.Fatalf("evidence %#v", prepared.NFO)
	}
	for _, path := range reader.paths {
		if strings.Contains(path, "screens.json") {
			t.Fatalf("adapter read %s", path)
		}
	}
	if !slices.Contains(reader.paths, env.NFOPath) {
		t.Fatalf("reader paths %q", reader.paths)
	}
}

// TestPrepareNeverReadsScreensJSON makes screens.json unreadable garbage: the
// adapter must still succeed because it never opens the file.
func TestPrepareNeverReadsScreensJSON(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	if err := os.WriteFile(env.ScreensPath, []byte{0xff, 0x00, 0xfe}, 0o000); err != nil {
		t.Fatal(err)
	}
	prepared, reader, err := prepare(t, env)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, path := range reader.paths {
		if filepath.Base(path) == filepath.Base(env.ScreensPath) {
			t.Fatal("screens.json was read")
		}
	}
	if strings.Contains(prepared.Description, zuleatest.DeleteSentinel) {
		t.Fatal("delete URL leaked into prepared inputs")
	}
}

func TestPrepareStops(t *testing.T) {
	cases := map[string]func(t *testing.T, env *zuleatest.Env){
		"mkv identity": func(t *testing.T, env *zuleatest.Env) {
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(env.MKVPath, later, later); err != nil {
				t.Fatal(err)
			}
		},
		"nfo changed": func(t *testing.T, env *zuleatest.Env) {
			data := append([]byte(nil), env.NFOBytes...)
			data[0] = 'X'
			if err := os.WriteFile(env.NFOPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"nfo release field": func(t *testing.T, env *zuleatest.Env) {
			if err := os.WriteFile(env.NFOPath, zuleatest.NFO("Other.Release-Zulea"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing torrent": func(t *testing.T, env *zuleatest.Env) {
			if err := os.Remove(env.TorrentPath); err != nil {
				t.Fatal(err)
			}
		},
		"torrent replaced": func(t *testing.T, env *zuleatest.Env) {
			data, err := os.ReadFile(env.TorrentPath)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.Replace(string(data), "zulea-tm-fixture", "zulea-tm-fixturX", 1))
			if err := os.WriteFile(env.TorrentPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"second mkv": func(t *testing.T, env *zuleatest.Env) {
			if err := os.WriteFile(filepath.Join(env.ReleaseDir, "extra.mkv"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"partial state": func(t *testing.T, env *zuleatest.Env) {
			if err := os.WriteFile(filepath.Join(env.ReleaseDir, "x.partial"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"description BOM": func(t *testing.T, env *zuleatest.Env) {
			path := filepath.Join(env.ReleaseDir, zuleatest.Release+".cinemamovies.txt")
			if err := os.WriteFile(path, []byte("\xef\xbb\xbf"+zuleatest.Description), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"description missing": func(t *testing.T, env *zuleatest.Env) {
			if err := os.Remove(filepath.Join(env.ReleaseDir, zuleatest.Release+".cinemamovies.txt")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := zuleatest.New(t, zuleatest.Options{})
			mutate(t, env)
			_, _, err := prepare(t, env)
			if !errors.Is(err, ErrStop) {
				t.Fatalf("expected STOP, got %v", err)
			}
			t.Logf("stop reason: %v", err)
		})
	}
}

func TestPrepareStopsOnManifestAndTorrentPolicy(t *testing.T) {
	for name, opts := range map[string]zuleatest.Options{
		"ptt not applied": {ManifestPTTStatus: "DRY_RUN_OK"},
		"torrent source":  {TorrentInfoExtra: map[string]any{"source": "PTT"}},
		"torrent entropy": {TorrentInfoExtra: map[string]any{"entropy": "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			env := zuleatest.New(t, opts)
			_, _, err := prepare(t, env)
			if !errors.Is(err, ErrStop) {
				t.Fatalf("expected STOP, got %v", err)
			}
			t.Logf("stop reason: %v", err)
		})
	}
}

func TestPrepareRejectsUnsafeReleaseNames(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	adapter, err := Open(sources(env))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", "..", "../" + zuleatest.Release, "a/b-Zulea", zuleatest.Release + "\x00"} {
		if _, err := adapter.Prepare(rel, PTT); !errors.Is(err, ErrStop) {
			t.Errorf("%q: expected STOP, got %v", rel, err)
		}
	}
}

// TestNewProfileNeedsNoAdapterChange shows a later tracker is pure data: a
// second profile reusing the same release inputs works unchanged.
func TestNewProfileNeedsNoAdapterChange(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	adapter, err := Open(sources(env))
	if err != nil {
		t.Fatal(err)
	}
	other := Profile{
		Tracker:           "OTHER",
		TMProfile:         "ptt",
		DescriptionSuffix: ".cinemamovies.txt",
		TorrentSuffix:     ".ptt.upload.torrent",
		Title:             func(r names.Result) string { return r.CinemaMovies },
	}
	prepared, err := adapter.Prepare(zuleatest.Release, other)
	if err != nil {
		t.Fatalf("prepare other profile: %v", err)
	}
	if !strings.Contains(prepared.Title, "[BluRay-Zulea]") {
		t.Fatalf("title %q", prepared.Title)
	}
	if _, ok := ProfileByTracker("ptt"); !ok {
		t.Fatal("PTT profile must be enabled")
	}
	if _, ok := ProfileByTracker("CM"); ok {
		t.Fatal("CinemaMovies is deferred and must not be enabled")
	}
}

// TestPrepareStopsOnPhysicalDisk covers W-2: the freeze physical_N5_disk must
// equal the configured disk and the file must really live on it.
func TestPrepareStopsOnPhysicalDisk(t *testing.T) {
	cases := map[string]func(env *zuleatest.Env) freeze.DiskCheck{
		"freeze names other disk": func(env *zuleatest.Env) freeze.DiskCheck {
			return freeze.DiskCheck{
				Disk:      "disk2",
				Mode:      freeze.DiskModePath,
				MountBase: env.MountBase,
			}
		},
		"library outside disk path": func(env *zuleatest.Env) freeze.DiskCheck {
			return freeze.DiskCheck{
				Disk:      zuleatest.Disk,
				Mode:      freeze.DiskModePath,
				MountBase: filepath.Join(env.Root, "elsewhere"),
			}
		},
		"xattr unavailable fails closed": func(_ *zuleatest.Env) freeze.DiskCheck {
			return freeze.DiskCheck{Disk: zuleatest.Disk, Mode: freeze.DiskModeUnraidXattr}
		},
	}
	for name, check := range cases {
		t.Run(name, func(t *testing.T) {
			env := zuleatest.New(t, zuleatest.Options{})
			src := sources(env)
			src.Disk = check(env)
			adapter, err := Open(src)
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Prepare(zuleatest.Release, PTT)
			if !errors.Is(err, ErrStop) {
				t.Fatalf("expected STOP, got %v", err)
			}
			t.Logf("stop reason: %v", err)
		})
	}
}

func TestOpenRejectsInvalidDiskCheck(t *testing.T) {
	env := zuleatest.New(t, zuleatest.Options{})
	for _, check := range []freeze.DiskCheck{{}, {Disk: "disk1"}, {Disk: "../x", Mode: freeze.DiskModePath}, {Disk: "disk1", Mode: "none"}} {
		src := sources(env)
		src.Disk = check
		if _, err := Open(src); err == nil {
			t.Errorf("%+v: expected error", check)
		}
	}
}
