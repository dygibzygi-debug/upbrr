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

const cliReport = "General\r\nComplete name                            : /srv/library/Example.Film-Zulea/Example.Film-Zulea.mkv\r\n" +
	"Format                                   : Matroska\r\n"

func TestCLIMediaInfoNormalizesAndWritesToWorkDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "mi")
	var gotArgs []string
	source := CLIMediaInfo{
		Binary:    "/usr/bin/mediainfo",
		OutputDir: out,
		Run: func(_ context.Context, binary string, args ...string) ([]byte, error) {
			gotArgs = append([]string{binary}, args...)
			return []byte(cliReport), nil
		},
	}
	path, err := source.MediaInfoText(context.Background(), "/srv/library/Example.Film-Zulea/Example.Film-Zulea.mkv", "Example.Film-Zulea")
	if err != nil {
		t.Fatalf("cli: %v", err)
	}
	if !pathing.IsWithinRoot(out, path) || len(gotArgs) != 3 || gotArgs[1] != "--Output=Text" {
		t.Fatalf("path %s args %q", path, gotArgs)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "/srv/library") || !strings.Contains(string(text), "Complete name                            : Example.Film-Zulea.mkv") ||
		strings.Contains(string(text), "\r") {
		t.Fatalf("not normalized: %q", text)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestMediaInfoRejectsOtherFile(t *testing.T) {
	source := CLIMediaInfo{
		Binary:    "mediainfo",
		OutputDir: t.TempDir(),
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(cliReport), nil
		},
	}
	if _, err := source.MediaInfoText(context.Background(), "/srv/library/Other-Zulea/Other-Zulea.mkv", "Other-Zulea"); err == nil {
		t.Fatal("report for another file must be rejected")
	}
	for _, bad := range []string{"Format : Matroska\n", "General\nComplete name : a.mkv\nComplete name : a.mkv\n", "General\n\xff\n"} {
		if _, err := normalizeMediaInfo(bad, "/x/a.mkv"); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestFileMediaInfo(t *testing.T) {
	dir := t.TempDir()
	good := "General\nComplete name                            : Example.Film-Zulea.mkv\nFormat : Matroska\n"
	if err := os.WriteFile(filepath.Join(dir, "Example.Film-Zulea.mediainfo.txt"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := FileMediaInfo{Dir: dir}.MediaInfoText(context.Background(), "/lib/Example.Film-Zulea/Example.Film-Zulea.mkv", "Example.Film-Zulea")
	if err != nil || filepath.Dir(path) != dir {
		t.Fatalf("file source: %s %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Raw-Zulea.mediainfo.txt"), []byte(cliReport), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileMediaInfo{Dir: dir}).MediaInfoText(context.Background(), "/x/Raw-Zulea.mkv", "Raw-Zulea"); err == nil {
		t.Fatal("unnormalized or foreign report must be rejected")
	}
	if _, err := (FileMediaInfo{Dir: dir}).MediaInfoText(context.Background(), "/x/Missing-Zulea.mkv", "Missing-Zulea"); err == nil {
		t.Fatal("missing report must be rejected")
	}
}
