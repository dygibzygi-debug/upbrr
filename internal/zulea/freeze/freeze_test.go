// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package freeze

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testRelease = "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.TrueHD.Atmos.7.1-Zulea"

func header() string { return strings.Join(Header, "\t") }

func row(fields ...string) string { return strings.Join(fields, "\t") }

func writeFreeze(t *testing.T, body string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "freeze.tsv")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return path, hex.EncodeToString(sum[:])
}

func TestLoadRequiresMatchingSHA(t *testing.T) {
	body := header() + "\n" + row("SRC", "disk1", testRelease, "eng,pol", "OK", "1", "2", "3", "100001") + "\n"
	path, sum := writeFreeze(t, body)
	if _, err := Load(path, ""); err == nil {
		t.Fatal("missing expected SHA must fail")
	}
	if _, err := Load(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong SHA must fail")
	}
	frozen, err := Load(path, sum)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if frozen.Len() != 1 || frozen.SHA256 != sum {
		t.Fatalf("unexpected freeze %#v", frozen)
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	path, sum := writeFreeze(t, header()+"\n")
	link := filepath.Join(filepath.Dir(path), "link.tsv")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(link, sum); err == nil {
		t.Fatal("symbolic link must be rejected")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":  "",
		"header": "SOURCE_ID\tRELEASE\n",
		"fields": header() + "\n" + row("SRC", "disk1", testRelease, "pol", "OK", "1", "2", "3") + "\n",
		"blank":  header() + "\n\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	crlf := header() + "\r\n" + row("SRC", "disk1", testRelease, "pol", "OK", "1", "2", "3", "") + "\r\n"
	if _, err := Parse([]byte(crlf)); err != nil {
		t.Errorf("CRLF rows are accepted like the wrapper: %v", err)
	}
}

func TestEligible(t *testing.T) {
	other := strings.Replace(testRelease, "2019", "2020", 1)
	dup := strings.Replace(testRelease, "2019", "2021", 1)
	bad := strings.Replace(testRelease, "2019", "2022", 1)
	body := strings.Join([]string{
		header(),
		row("SRC/a b", "disk1", testRelease, "eng,pol", "OK", "11", "22", "33", "100001"),
		row("SRC", "disk1", other, "pol", "WYJATEK", "1", "2", "3", ""),
		row("SRC", "disk1", dup, "pol", "OK", "1", "2", "3", ""),
		row("SRC", "disk2", dup, "pol", "OK", "1", "2", "3", ""),
		row("SRC", "disk1", bad, "pol", "OK", "x", "2", "3", ""),
	}, "\n") + "\n"
	frozen, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := frozen.Eligible(testRelease)
	if err != nil || rec.SourceID != "SRC/a b" || rec.TMDBID != "100001" {
		t.Fatalf("eligible record: %#v %v", rec, err)
	}
	for _, rel := range []string{other, dup, bad, "Missing-Zulea"} {
		if _, err := frozen.Eligible(rel); !errors.Is(err, ErrNotEligible) {
			t.Errorf("%s: expected ErrNotEligible, got %v", rel, err)
		}
	}
}

// TestVerifyMKVStopsOnIdentityMismatch covers the mandatory STOP when the MKV
// stat identity differs from the freeze.
func TestVerifyMKVStopsOnIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	mkv := filepath.Join(dir, testRelease+".mkv")
	if err := os.WriteFile(mkv, []byte("synthetic-mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1_700_000_000, 999_000_000)
	if err := os.Chtimes(mkv, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	got, err := StatIdentity(mkv)
	if err != nil {
		t.Skipf("inode identity unavailable: %v", err)
	}
	if got.Mtime != 1_700_000_000 || got.Size != int64(len("synthetic-mkv")) {
		t.Fatalf("identity %v", got)
	}
	rec := Record{
		Release:  testRelease,
		Status:   StatusOK,
		MKVIno:   strconv.FormatUint(got.Ino, 10),
		MKVSize:  strconv.FormatInt(got.Size, 10),
		MKVMtime: strconv.FormatInt(got.Mtime, 10),
	}
	if _, err := VerifyMKV(rec, mkv); err != nil {
		t.Fatalf("matching identity: %v", err)
	}
	for name, mutate := range map[string]func(*Record){
		"ino":   func(r *Record) { r.MKVIno += "1" },
		"size":  func(r *Record) { r.MKVSize = "1" },
		"mtime": func(r *Record) { r.MKVMtime = "1700000001" },
	} {
		changed := rec
		mutate(&changed)
		if _, err := VerifyMKV(changed, mkv); !errors.Is(err, ErrNotEligible) {
			t.Errorf("%s mismatch: expected ErrNotEligible, got %v", name, err)
		}
	}
	link := filepath.Join(dir, "link.mkv")
	if err := os.Symlink(mkv, link); err == nil {
		if _, err := VerifyMKV(rec, link); err == nil {
			t.Error("symlinked MKV must be rejected")
		}
	}
}
