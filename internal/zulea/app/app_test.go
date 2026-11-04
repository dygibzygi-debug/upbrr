// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const (
	tmdbKey = "TMDBKEYSENTINEL0123456789"
	pttKey  = "PTTAPIKEYSENTINEL0123456789"
)

type fakeMediaInfo struct{ dir string }

func (f fakeMediaInfo) MediaInfoText(_ context.Context, mkvPath string, release string) (string, error) {
	path := filepath.Join(f.dir, release+".mediainfo.txt")
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return "", fmt.Errorf("fake mediainfo dir: %w", err)
	}
	if err := os.WriteFile(path, []byte("General\nComplete name : "+filepath.Base(mkvPath)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("fake mediainfo write: %w", err)
	}
	return path, nil
}

// guardTransport fails the test on any HTTP request except the local TMDB fake.
type guardTransport struct {
	t       *testing.T
	allowed string
	mu      sync.Mutex
	hosts   []string
}

func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.hosts = append(g.hosts, req.URL.Host+req.URL.Path)
	g.mu.Unlock()
	if req.URL.Host != g.allowed {
		g.t.Errorf("unexpected network request to %s", req.URL.Host)
		return nil, http.ErrHandlerTimeout
	}
	resp, err := httpTransport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("guard transport: %w", err)
	}
	return resp, nil
}

var httpTransport = http.DefaultTransport

func writePrivate(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (*zuleatest.Env, Config, *guardTransport) {
	t.Helper()
	env := zuleatest.New(t, zuleatest.Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/"+zuleatest.TMDBID || r.URL.Query().Get("api_key") != tmdbKey {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":100001,"imdb_id":"tt0000456","original_language":"en","keywords":{"keywords":[{"name":"music"}]}}`))
	}))
	t.Cleanup(server.Close)
	guard := &guardTransport{t: t, allowed: strings.TrimPrefix(server.URL, "http://")}
	http.DefaultTransport = guard
	t.Cleanup(func() { http.DefaultTransport = httpTransport })

	tmdbFile := filepath.Join(env.Root, "tmdb.key")
	pttFile := filepath.Join(env.Root, "ptt.key")
	writePrivate(t, tmdbFile, tmdbKey+"\n")
	writePrivate(t, pttFile, pttKey+"\n")
	cfg := Config{
		LibraryRoot:  env.LibraryRoot,
		FreezePath:   env.FreezePath,
		FreezeSHA256: env.FreezeSHA256,
		TitlesPath:   env.TitlesPath,
		ManifestPath: env.ManifestPath,
		TMOutputDir:  env.TorrentDir,
		WorkDir:      filepath.Join(env.Root, "work"),
		TMDBKeyFile:  tmdbFile,
		TMDBBaseURL:  server.URL,
		Trackers:     []string{"PTT"},
		PTT:          PTTConfig{APIKeyFile: pttFile},
	}
	return env, cfg, guard
}

func TestDryRunEndToEnd(t *testing.T) {
	env, cfg, guard := setup(t)
	var logs bytes.Buffer
	logger := NewLogger(&logs, true)
	runner := &Runner{
		Config:    cfg,
		Logger:    logger,
		MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")},
	}
	missing := strings.Replace(zuleatest.Release, "2019", "2018", 1)
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release, missing})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].Status != StatusReady || outcomes[1].Status != StatusStop {
		t.Fatalf("outcomes %#v", outcomes)
	}
	var out bytes.Buffer
	Render(&out, outcomes, logger.MaskKnown)
	if !strings.Contains(out.String(), "READY=1 STOP=1") || !strings.Contains(out.String(), "nic nie zostało wysłane") {
		t.Fatalf("render:\n%s", out.String())
	}
	if !strings.Contains(out.String(), outcomes[0].Plan.Title) || !strings.Contains(out.String(), "[b]SCREENY[/b]") {
		t.Fatal("preview must show the title and the full description")
	}
	// Exactly one network request: TMDB by the frozen id. No SRRDB, tracker or image host.
	if len(guard.hosts) != 1 || !strings.HasSuffix(guard.hosts[0], "/movie/"+zuleatest.TMDBID) {
		t.Fatalf("network requests %q", guard.hosts)
	}
	report, err := os.ReadFile(outcomes[0].ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(outcomes[0].ReportPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("report mode %v", st.Mode().Perm())
	}
	var decoded Outcome
	if err := json.Unmarshal(report, &decoded); err != nil || decoded.Plan.Fingerprint != outcomes[0].Plan.Fingerprint {
		t.Fatalf("report decode: %v", err)
	}
	for _, secret := range []string{tmdbKey, pttKey, zuleatest.PasskeySentinel, zuleatest.DeleteSentinel} {
		for name, text := range map[string]string{
			"stdout": out.String(),
			"log":    logs.String(),
			"report": string(report),
		} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s leaks %s", name, secret)
			}
		}
	}
	entries, err := os.ReadDir(env.ReleaseDir)
	if err != nil || len(entries) != 5 {
		t.Fatalf("library changed: %d entries", len(entries))
	}
}

func TestDryRunRequiresEnabledTracker(t *testing.T) {
	_, cfg, _ := setup(t)
	cfg.Trackers = nil
	if _, err := (&Runner{Config: cfg}).DryRun(context.Background(), []string{zuleatest.Release}); err == nil {
		t.Fatal("no enabled tracker must fail")
	}
}

func TestLoadConfig(t *testing.T) {
	_, cfg, _ := setup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "zulea.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, string(data))
	loaded, err := LoadConfig(path)
	if err != nil || loaded.LibraryRoot != cfg.LibraryRoot {
		t.Fatalf("load: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("world-readable config must be rejected")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, `{"library_root":"/x","api_key":"inline-secret"}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown fields (e.g. inline secrets) must be rejected")
	}
	invalid := map[string]func(*Config){
		"overlap":        func(c *Config) { c.WorkDir = filepath.Join(c.LibraryRoot, "work") },
		"relative":       func(c *Config) { c.WorkDir = "work" },
		"other tracker":  func(c *Config) { c.Trackers = []string{"PTT", "BLU"} },
		"no tmdb key":    func(c *Config) { c.TMDBKeyFile = "" },
		"no freeze sha":  func(c *Config) { c.FreezeSHA256 = "" },
		"library inside": func(c *Config) { c.LibraryRoot = filepath.Join(c.WorkDir, "lib") },
	}
	for name, mutate := range invalid {
		c := cfg
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestKeyFilesMustBePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("abc\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyFile(path); err == nil {
		t.Fatal("group-readable key file must be rejected")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := readKeyFile(path); err != nil || key != "abc" {
		t.Fatalf("key %q err %v", key, err)
	}
	writePrivate(t, path, "a b\n")
	if _, err := readKeyFile(path); err == nil {
		t.Fatal("multi-token key file must be rejected")
	}
}

func TestLoggerMasksSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, true)
	logger.AddSecret(pttKey)
	logger.Infof("request key=%s url=https://x.invalid/announce/%s", pttKey, zuleatest.PasskeySentinel)
	if strings.Contains(buf.String(), pttKey) || strings.Contains(buf.String(), zuleatest.PasskeySentinel) {
		t.Fatalf("log leaks: %s", buf.String())
	}
	buf.Reset()
	digest := strings.Repeat("ab", 20)
	logger.Evidencef("infohash=%s key=%s", digest, pttKey)
	if !strings.Contains(buf.String(), digest) || strings.Contains(buf.String(), pttKey) {
		t.Fatalf("evidence line must keep digests and mask secrets: %s", buf.String())
	}
}
