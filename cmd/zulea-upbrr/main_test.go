// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/app"
	"github.com/autobrr/upbrr/internal/zulea/clilock"
)

func TestSubmitAliasesAreRefused(t *testing.T) {
	for _, cmd := range []string{"submit", "send"} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{cmd}, &out, &errOut); code != exitUsage {
			t.Fatalf("%s: exit %d", cmd, code)
		}
	}
}

// TestUploadRequiresTerminal: piping a confirmation is impossible.
func TestUploadRequiresTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{"upload", "--config", "x.json", "--release", "R-Zulea", "--fingerprint", strings.Repeat("a", 64)}
	if code := run(context.Background(), args, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "interaktywny terminal") {
		t.Fatalf("exit %d err %q", code, errOut.String())
	}
	if code := run(context.Background(), []string{"upload"}, &out, &errOut); code != exitUsage {
		t.Fatalf("missing flags exit %d", code)
	}
}

// TestResolveRequiresTerminal: a receipt decision cannot be scripted either.
func TestDupeAcceptRequiresTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{"dupe-accept", "--config", "x.json", "--release", "R-Zulea", "--reason", "REMUX vs encode"}
	if code := run(context.Background(), args, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "interaktywny terminal") {
		t.Fatalf("exit %d err %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	forced := []string{"dupe-accept", "--config", "x.json", "--release", "R-Zulea", "--reason", "REMUX vs encode", "--force"}
	if code := run(context.Background(), forced, &out, &errOut); code != exitUsage || strings.Contains(errOut.String(), "interaktywny terminal") {
		t.Fatalf("--force must be rejected before any prompt, exit %d err %q", code, errOut.String())
	}
}

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "0.7.1") {
		t.Fatalf("exit %d out %q err %q", code, out.String(), errOut.String())
	}
}

func TestResolveRequiresTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{"resolve", "--config", "x.json", "--release", "R-Zulea", "--state", "not_sent", "--note", "x"}
	if code := run(context.Background(), args, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "interaktywny terminal") {
		t.Fatalf("exit %d err %q", code, errOut.String())
	}
}

func TestCompareTorrentCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"compare-torrent", "only-one"}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestNamesCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"names", "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea"}, &out, &errOut)
	if code != exitOK || !strings.Contains(out.String(), "PTT: Example Film 2019 MULTi 1080p BluRay REMUX AVC DTS-HD MA 5.1-Zulea") {
		t.Fatalf("exit %d out %q err %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"names", "Bad-GRP"}, &out, &errOut); code != exitStop {
		t.Fatalf("invalid name exit %d", code)
	}
}

func TestScanDelayMinimum(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"scan-ptt", "--config", "x.json", "--delay", "1s"}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "3s") {
		t.Fatalf("exit %d err %q", code, errOut.String())
	}
}

func TestPlanRequiresConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"plan", "--release", "X"}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestOfflineCommandsRequireConfig(t *testing.T) {
	for _, cmd := range []string{"check", "verify-identity", "receipts"} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{cmd}, &out, &errOut); code != exitUsage {
			t.Fatalf("%s: exit %d", cmd, code)
		}
	}
}

func TestGlobalLockCommandSet(t *testing.T) {
	locked := []string{"plan", "dry-run", "upload", "dupe-accept", "resolve", "seed", "ptt-status", "scan-ptt", "check", "summary", "history"}
	for _, command := range locked {
		if !commandNeedsGlobalLock(command) {
			t.Errorf("%s must use the global lock", command)
		}
	}
	for _, command := range []string{"version", "receipts", "queue", "seed-status"} {
		if commandNeedsGlobalLock(command) {
			t.Errorf("%s must stay readable while the lock is held", command)
		}
	}
}

func TestBusyLockReturns75AndStableJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LoadConfig requires POSIX 0600; the production and lock contract are Linux-only")
	}
	work := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "zulea.json")
	cfg := app.Config{
		LibraryRoot:   filepath.Join(t.TempDir(), "library"),
		FreezePath:    filepath.Join(t.TempDir(), "freeze.tsv"),
		FreezeSHA256:  strings.Repeat("0", 64),
		TitlesPath:    filepath.Join(t.TempDir(), "titles.tsv"),
		ManifestPath:  filepath.Join(t.TempDir(), "manifest.tsv"),
		TMOutputDir:   filepath.Join(t.TempDir(), "torrents"),
		WorkDir:       work,
		PhysicalDisk:  "disk1",
		DiskCheckMode: "path",
		MediaInfo: app.MediaInfoConfig{
			Source: app.MediaInfoCLI,
			Binary: "mediainfo",
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := clilock.Acquire(work)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"summary", "--config", configPath, "--json"}, &out, &errOut)
	if code != exitBusy || errOut.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	var doc app.JSONDocument
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.Command != "summary" || doc.OK || doc.ExitCode != exitBusy || doc.Error != "busy" {
		t.Fatalf("busy JSON: %+v", doc)
	}
}
