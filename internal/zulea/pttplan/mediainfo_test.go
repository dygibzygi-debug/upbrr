// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pttplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/pathing"
)

// TestUpbrrMediaInfoWritesOutsideLibrary analyzes a committed synthetic MKV
// from a read-only copy and checks the report lands only in the work directory.
func TestUpbrrMediaInfoWritesOutsideLibrary(t *testing.T) {
	src := filepath.Join("..", "..", "..", "cmd", "upbrr", "testdata", "synthetic-cli-hdr.mkv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("synthetic MKV unavailable: %v", err)
	}
	root := t.TempDir()
	library := filepath.Join(root, "library", "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS.5.1-Zulea")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(library, "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS.5.1-Zulea.mkv")
	if err := os.WriteFile(mkv, data, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(library, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(library, 0o755) })
	work := filepath.Join(root, "work", "mediainfo")
	path, err := UpbrrMediaInfo{TempRoot: work}.MediaInfoText(context.Background(), mkv, "Example")
	if err != nil {
		t.Fatalf("mediainfo: %v", err)
	}
	if !pathing.IsWithinRoot(work, path) {
		t.Fatalf("report %s outside work dir", path)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "Matroska") || strings.Contains(string(text), library) {
		t.Fatalf("unexpected MediaInfo text: %.300s", text)
	}
	entries, err := os.ReadDir(library)
	if err != nil || len(entries) != 1 {
		t.Fatalf("library changed: %d entries err=%v", len(entries), err)
	}
}
