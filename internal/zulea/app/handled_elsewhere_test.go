// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

func writeHandledFile(t *testing.T, path string, entries []HandledElsewhereEntry) {
	t.Helper()
	data, err := json.Marshal(handledElsewhereFile{SchemaVersion: 1, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func handledEntry(release string) HandledElsewhereEntry {
	id := 36025
	return HandledElsewhereEntry{
		Release: release, Tracker: "PTT", TorrentID: &id,
		Tool: "upbrr", Added: "2026-11-15", Note: "sent outside this tool",
	}
}

func TestHandledElsewhereMissingAndStrictSchema(t *testing.T) {
	runner := &Runner{Config: Config{WorkDir: t.TempDir()}, HandledElsewherePath: filepath.Join(t.TempDir(), "missing.json")}
	entries, err := runner.loadHandledElsewhere()
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing list: entries=%v err=%v", entries, err)
	}
	path := filepath.Join(t.TempDir(), "handled.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"entries":[],"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.HandledElsewherePath = path
	if _, err := runner.loadHandledElsewhere(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field must fail: %v", err)
	}
	outcomes, err := runner.DryRun(context.Background(), []string{"Example.2001.1080p-GRP"})
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != StatusStop || !strings.Contains(outcomes[0].Reason, "invalid JSON") {
		t.Fatalf("malformed list must make plan STOP: outcomes=%+v err=%v", outcomes, err)
	}
}

func TestHandledElsewhereStopsBeforeNetwork(t *testing.T) {
	_, cfg, guard := setup(t)
	path := filepath.Join(t.TempDir(), "handled.json")
	entry := handledEntry(zuleatest.Release)
	writeHandledFile(t, path, []HandledElsewhereEntry{entry})
	runner := &Runner{Config: cfg, Logger: NewLogger(io.Discard, false), HandledElsewherePath: path}

	outcomes, err := runner.DryRun(context.Background(), []string{zuleatest.Release})
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != StatusStop || outcomes[0].HandledElsewhere == nil {
		t.Fatalf("plan outcome=%+v err=%v", outcomes, err)
	}
	if len(guard.hosts) != 0 {
		t.Fatalf("handled plan contacted network: %v", guard.hosts)
	}
	if !strings.Contains(outcomes[0].Reason, "obsłużone innym narzędziem") {
		t.Fatalf("reason %q", outcomes[0].Reason)
	}

	if _, err := runner.Upload(context.Background(), UploadRequest{Release: zuleatest.Release}); err == nil || !strings.Contains(err.Error(), "obsłużone") {
		t.Fatalf("upload must stop on local list: %v", err)
	}
	if _, err := runner.DupeAccept(DupeAcceptRequest{Release: zuleatest.Release}); err == nil || !strings.Contains(err.Error(), "obsłużone") {
		t.Fatalf("dupe-accept must stop on local list: %v", err)
	}
	if _, err := runner.Resolve(ResolveRequest{Release: zuleatest.Release}); err == nil || !strings.Contains(err.Error(), "obsłużone") {
		t.Fatalf("resolve must stop on local list: %v", err)
	}
	if _, err := runner.Seed(context.Background(), SeedRequest{Release: zuleatest.Release}, nil); err == nil || !strings.Contains(err.Error(), "obsłużone") {
		t.Fatalf("seed must stop on local list: %v", err)
	}
	if len(guard.hosts) != 0 {
		t.Fatalf("blocked commands contacted network: %v", guard.hosts)
	}
}

func TestHandledElsewhereCheckAndValidation(t *testing.T) {
	_, cfg, _ := setup(t)
	path := filepath.Join(t.TempDir(), "handled.json")
	writeHandledFile(t, path, []HandledElsewhereEntry{handledEntry(zuleatest.Release)})
	runner := &Runner{Config: cfg, HandledElsewherePath: path}

	summary, err := runner.Check([]string{zuleatest.Release})
	if err != nil || summary.HandledElsewhere != 1 || len(summary.Results) != 1 || summary.Results[0].Status != StatusHandledElsewhere {
		t.Fatalf("check summary=%+v err=%v", summary, err)
	}
	validation, err := runner.ValidateHandledElsewhere()
	if err != nil || validation.Entries != 1 || len(validation.Warnings) != 0 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}

	if err := os.MkdirAll(filepath.Dir(runner.receiptPath(zuleatest.Release)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.receiptPath(zuleatest.Release), []byte(`{"release":"`+zuleatest.Release+`","tracker":"PTT","state":"accepted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ValidateHandledElsewhere(); err == nil || !strings.Contains(err.Error(), "also has") {
		t.Fatalf("receipt conflict must fail: %v", err)
	}
}

func TestHandledElsewhereUnknownReleaseWarning(t *testing.T) {
	_, cfg, _ := setup(t)
	path := filepath.Join(t.TempDir(), "handled.json")
	writeHandledFile(t, path, []HandledElsewhereEntry{handledEntry("Unknown.2001.1080p-GRP")})
	runner := &Runner{Config: cfg, HandledElsewherePath: path}
	validation, err := runner.ValidateHandledElsewhere()
	if err != nil || len(validation.Warnings) != 2 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
}

func TestHandledElsewhereHasSeparateQueueState(t *testing.T) {
	_, cfg, _ := setup(t)
	path := filepath.Join(t.TempDir(), "handled.json")
	writeHandledFile(t, path, []HandledElsewhereEntry{handledEntry(zuleatest.Release)})
	runner := &Runner{Config: cfg, HandledElsewherePath: path}
	reports := filepath.Join(cfg.WorkDir, "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	scan := ScanSummary{Results: []ScanResult{{Release: zuleatest.Release, Chain: "OK", Title: "Synthetic title", Dupe: "CLEAR", SearchComplete: true}}}
	data, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "scan-20261116-000000.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := runner.Queue()
	if err != nil || len(entries) != 1 || entries[0].State != StatusHandledElsewhere || entries[0].HandledElsewhere == nil {
		t.Fatalf("queue=%+v err=%v", entries, err)
	}
}
