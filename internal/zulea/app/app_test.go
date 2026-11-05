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
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
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

// guardTransport fails the test on any HTTP request except the local TMDB
// fake and polishtorrent.top, which it rewrites to the local PTT fake.
type guardTransport struct {
	t       *testing.T
	allowed string
	ptt     *url.URL
	mu      sync.Mutex
	hosts   []string
}

func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.hosts = append(g.hosts, req.URL.Hostname()+req.URL.Path)
	g.mu.Unlock()
	switch {
	case req.URL.Host == g.allowed:
	case req.URL.Hostname() == "polishtorrent.top" && g.ptt != nil:
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host, req.Host = g.ptt.Scheme, g.ptt.Host, g.ptt.Host
	default:
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

// pttRows is the JSON row list the fake PTT search returns (reset per setup).
var pttRows string

func completeCanaryConfig(cfg Config) Config {
	cfg.PTT.Taxonomy = &pttplan.Taxonomy{
		Category:   map[string]string{"MOVIE": "1"},
		Type:       map[string]string{"REMUX": "2"},
		Resolution: map[string]string{"1080p": "3"},
	}
	cfg.PTT.PersonalRelease = new(true)
	cfg.PTT.ModQueue = true
	return cfg
}

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
	pttServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/torrents/filter" || r.Header.Get("Authorization") != "Bearer "+pttKey {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[` + pttRows + `],"links":{"next":null},"meta":{"current_page":1,"last_page":1}}`))
	}))
	t.Cleanup(pttServer.Close)
	pttURL, err := url.Parse(pttServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	pttRows = ""
	guard := &guardTransport{
		t:       t,
		allowed: strings.TrimPrefix(server.URL, "http://"),
		ptt:     pttURL,
	}
	http.DefaultTransport = guard
	t.Cleanup(func() { http.DefaultTransport = httpTransport })

	tmdbFile := filepath.Join(env.Root, "tmdb.key")
	pttFile := filepath.Join(env.Root, "ptt.key")
	writePrivate(t, tmdbFile, tmdbKey+"\n")
	writePrivate(t, pttFile, pttKey+"\n")
	cfg := Config{
		LibraryRoot:   env.LibraryRoot,
		FreezePath:    env.FreezePath,
		FreezeSHA256:  env.FreezeSHA256,
		TitlesPath:    env.TitlesPath,
		ManifestPath:  env.ManifestPath,
		TMOutputDir:   env.TorrentDir,
		WorkDir:       filepath.Join(env.Root, "work"),
		TMDBKeyFile:   tmdbFile,
		TMDBBaseURL:   server.URL,
		PhysicalDisk:  zuleatest.Disk,
		DiskCheckMode: freeze.DiskModePath,
		MountBase:     env.MountBase,
		MediaInfo:     MediaInfoConfig{Source: MediaInfoCLI, Binary: "/usr/bin/mediainfo"},
		Trackers:      []string{"PTT"},
		PTT:           PTTConfig{APIKeyFile: pttFile},
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
	// Exactly two requests: TMDB by the frozen id (once, shared by plan and
	// duplicate check) and the PTT duplicate search. No SRRDB or image host.
	if len(guard.hosts) != 2 || !strings.HasSuffix(guard.hosts[0], "/movie/"+zuleatest.TMDBID) ||
		guard.hosts[1] != "polishtorrent.top/api/torrents/filter" {
		t.Fatalf("network requests %q", guard.hosts)
	}
	if outcomes[0].Dupe == nil || outcomes[0].Dupe.Status != "CLEAR" {
		t.Fatalf("dupe %+v", outcomes[0].Dupe)
	}
	if outcomes[0].Canary.Ready || len(outcomes[0].Canary.Blockers) != 3 {
		t.Fatalf("incomplete config must block canary (W-1, D-5 x2): %+v", outcomes[0].Canary)
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
		"overlap":            func(c *Config) { c.WorkDir = filepath.Join(c.LibraryRoot, "work") },
		"relative":           func(c *Config) { c.WorkDir = "work" },
		"other tracker":      func(c *Config) { c.Trackers = []string{"PTT", "BLU"} },
		"no tmdb key":        func(c *Config) { c.TMDBKeyFile = "" },
		"no freeze sha":      func(c *Config) { c.FreezeSHA256 = "" },
		"library inside":     func(c *Config) { c.LibraryRoot = filepath.Join(c.WorkDir, "lib") },
		"no disk":            func(c *Config) { c.PhysicalDisk = "" },
		"no tm source":       func(c *Config) { c.ManifestPath, c.TMOutputDir = "", "" },
		"both tm sources":    func(c *Config) { c.TMRunsDir, c.TMOutputBase = "/r", "/o" },
		"partial tm runs":    func(c *Config) { c.ManifestPath, c.TMOutputDir, c.TMRunsDir = "", "", "/r" },
		"no disk mode":       func(c *Config) { c.DiskCheckMode = "" },
		"no mediainfo":       func(c *Config) { c.MediaInfo = MediaInfoConfig{} },
		"cli without binary": func(c *Config) { c.MediaInfo = MediaInfoConfig{Source: MediaInfoCLI} },
		"file dir in library": func(c *Config) {
			c.MediaInfo = MediaInfoConfig{Source: MediaInfoFile, Dir: filepath.Join(c.LibraryRoot, "mi")}
		},
		"qbit relative save": func(c *Config) {
			c.QBittorrent = &QBitConfig{
				Host:         "http://qbittorrent.invalid",
				Username:     "u",
				PasswordFile: "/secrets/qbit",
				SavePath:     "library",
			}
		},
		"qbit category path": func(c *Config) {
			c.QBittorrent = &QBitConfig{
				Host:         "http://qbittorrent.invalid",
				Username:     "u",
				PasswordFile: "/secrets/qbit",
				SavePath:     "/data/library",
				Category:     "a/b",
			}
		},
	}
	for name, mutate := range invalid {
		c := cfg
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	cfg.QBittorrent = &QBitConfig{
		Host:         "http://qbittorrent.invalid",
		Username:     "u",
		PasswordFile: "/secrets/qbit",
		SavePath:     "/data/library",
		Category:     "examples",
	}
	cfg.Nightly = &NightlyConfig{}
	if err := cfg.Validate(); err != nil || cfg.NightlyScanEnabled() {
		t.Fatalf("qbit config and nightly default: %v enabled=%t", err, cfg.NightlyScanEnabled())
	}
	cfg.Nightly.ScanPTT = true
	if !cfg.NightlyScanEnabled() {
		t.Fatal("nightly scan must be opt-in")
	}
}

func TestKeyFilesMustBePrivate(t *testing.T) {
	// The 0600 contract is Linux-only. Windows reports 0666 for every
	// writable file because os.Chmod only toggles the read-only attribute,
	// so a private key file cannot be distinguished there. The uploader
	// runs on Linux, where this mode is enforced.
	if runtime.GOOS == "windows" {
		t.Skip("file mode 0600 is enforced on Linux; Windows does not store Unix permission bits")
	}
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

func TestCanaryReadyWithCompleteDecisions(t *testing.T) {
	_, cfg, _ := setup(t)
	runner := &Runner{Config: completeCanaryConfig(cfg), MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil {
		t.Fatal(err)
	}
	if !outcomes[0].Canary.Ready || len(outcomes[0].Canary.Blockers) != 0 || !outcomes[0].Plan.TaxonomyConfirmed {
		t.Fatalf("canary gate %+v", outcomes[0].Canary)
	}
	if outcomes[0].Plan.Payload["mod_queue_opt_in"] != "1" || outcomes[0].Plan.Payload["personal_release"] != "1" {
		t.Fatalf("D-5 fields %q %q", outcomes[0].Plan.Payload["mod_queue_opt_in"], outcomes[0].Plan.Payload["personal_release"])
	}
}

func TestDuplicateOnPTTStopsRelease(t *testing.T) {
	_, cfg, _ := setup(t)
	pttRows = `{"id":7,"attributes":{"name":"Example Film 2019 MULTi 1080p BluRay REMUX AVC TrueHD Atmos 7.1-GRP",` +
		`"tmdb_id":100001,"category":"Movie","type":"Remux","resolution":"1080p","size":40}}`
	runner := &Runner{Config: completeCanaryConfig(cfg), MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil {
		t.Fatal(err)
	}
	o := outcomes[0]
	if o.Status != StatusStop || o.Dupe == nil || o.Dupe.Status != "DUPLICATE" || o.Canary.Ready {
		t.Fatalf("duplicate must stop the release: %+v", o)
	}
	var out bytes.Buffer
	Render(&out, outcomes, nil)
	if !strings.Contains(out.String(), "[same_slot]") {
		t.Fatalf("render must list the duplicate:\n%s", out.String())
	}
}

func TestCanaryBlocksUpbrrMediaInfo(t *testing.T) {
	_, cfg, _ := setup(t)
	cfg = completeCanaryConfig(cfg)
	cfg.MediaInfo = MediaInfoConfig{Source: MediaInfoUpbrr}
	gate := (&Runner{Config: cfg}).canaryGate(zuleatest.Release, &pttplan.Plan{TaxonomyConfirmed: true}, nil, "k")
	if gate.Ready || !strings.Contains(strings.Join(gate.Blockers, "|"), "D-6") {
		t.Fatalf("gate %+v", gate)
	}
}

func TestDryRunWithAllTMRuns(t *testing.T) {
	env, cfg, _ := setup(t)
	cfg.ManifestPath, cfg.TMOutputDir = "", ""
	cfg.TMRunsDir = filepath.Dir(filepath.Dir(env.ManifestPath))
	cfg.TMOutputBase = filepath.Dir(env.TorrentDir)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Config: cfg, MediaInfo: fakeMediaInfo{dir: filepath.Join(cfg.WorkDir, "mi")}}
	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || outcomes[0].Status != StatusReady {
		t.Fatalf("multi-run dry-run: %+v %v", outcomes, err)
	}
}
