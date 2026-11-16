// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const qbitPasswordSentinel = "QBIT-PASSWORD-SENTINEL-0600"

type fakeWeb struct {
	version string
	exists  bool
	auto    bool
}

func (f fakeWeb) Login(context.Context) error { return nil }
func (f fakeWeb) Inspect(context.Context, string) (seed.WebUIReport, error) {
	return seed.WebUIReport{
		Version:        f.version,
		SavePathExists: f.exists,
		AutoTMM:        f.auto,
	}, nil
}

func TestCheckQBitWarnsWhenAutoTMMIsOn(t *testing.T) {
	_, cfg, _ := setup(t)
	pass := filepath.Join(t.TempDir(), "qb.password")
	writePrivate(t, pass, qbitPasswordSentinel+"\n")
	cfg.QBittorrent = &QBitConfig{
		Host:         "http://qbittorrent.invalid",
		Username:     "zulea",
		PasswordFile: pass,
		SavePath:     "/data/library",
		Category:     "examples",
	}
	var logs bytes.Buffer
	runner := &Runner{Config: cfg, Logger: NewLogger(&logs, true)}
	probe, err := runner.CheckQBit(context.Background(), fakeWeb{
		version: "v5.0.0",
		exists:  true,
		auto:    true,
	})
	if err != nil || len(probe.Warnings) != 1 || !strings.Contains(probe.Warnings[0], "auto_tmm_enabled") {
		t.Fatalf("probe %+v %v", probe, err)
	}
	if _, err := runner.CheckQBit(context.Background(), fakeWeb{version: "v5.0.0", exists: false}); err == nil {
		t.Fatal("a missing save path must fail the check")
	}
	if strings.Contains(logs.String(), qbitPasswordSentinel) {
		t.Fatalf("log leaked the password: %s", logs.String())
	}
}

func TestSeedUsesPTTHashAndHidesThePassword(t *testing.T) {
	_, _, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	uploaded, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	})
	if err != nil {
		t.Fatal(err)
	}
	pttHash := uploaded.Comparison.Downloaded.InfoHash
	var mu sync.Mutex
	sawPassword, mutated := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if strings.Contains(string(body), qbitPasswordSentinel) {
				sawPassword = true
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash":      pttHash,
				"name":      zuleatest.Release,
				"progress":  1,
				"state":     "uploading",
				"save_path": "/data/library",
				"category":  "kept",
			}})
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.0.0"))
		case "/api/v2/app/webapiVersion":
			_, _ = w.Write([]byte("2.11.2"))
		case "/api/v2/app/preferences":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auto_tmm_enabled":true,"dyndns_password":"` + qbitPasswordSentinel + `"}`))
		case "/api/v2/app/getDirectoryContent":
			if r.URL.Query().Get("dirPath") != "/data/library" {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			mutated = true
			http.Error(w, "no", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	pass := filepath.Join(t.TempDir(), "qb.password")
	writePrivate(t, pass, qbitPasswordSentinel+"\n")
	runner.Config.QBittorrent = &QBitConfig{
		Host:         server.URL,
		Username:     "zulea",
		PasswordFile: pass,
		SavePath:     "/data/library",
		Category:     "examples",
	}
	var logs bytes.Buffer
	runner.Logger = NewLogger(&logs, true)
	res, err := runner.Seed(context.Background(), SeedRequest{Release: zuleatest.Release, Start: true}, nil)
	if err != nil || res.Added || res.SeedSource != seed.SeedSourceManual || res.Hash != pttHash || res.Hash == uploaded.Receipt.InfoHash {
		t.Fatalf("seed %+v err=%v tm=%s", res, err, uploaded.Receipt.InfoHash)
	}
	probe, err := runner.CheckQBit(context.Background(), nil)
	if err != nil || probe.Version != "v5.0.0" || len(probe.Warnings) != 1 {
		t.Fatalf("check %+v %v", probe, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawPassword || mutated {
		t.Fatalf("password sent=%t mutating call=%t", sawPassword, mutated)
	}
	blob, err := os.ReadFile(runner.receiptPath(zuleatest.Release))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{logs.String(), res.Message, string(blob), probe.Version, strings.Join(probe.Warnings, "\n")} {
		if strings.Contains(text, qbitPasswordSentinel) {
			t.Fatal("secret leaked into logs, the receipt or the qBittorrent report")
		}
	}
}
