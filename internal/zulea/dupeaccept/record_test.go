// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package dupeaccept

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
)

func candidate(id, name string, size int64) pttdupe.Candidate {
	return pttdupe.Candidate{
		ID:         id,
		Name:       name,
		Type:       "Encode",
		Resolution: "1080p",
		SizeBytes:  size,
		Relation:   "same_slot",
		Reasons:    []string{"same_tracker_slot: Candidate occupies the same tracker slot and requires review."},
	}
}

func TestSetSHATracksShownFields(t *testing.T) {
	base := []pttdupe.Candidate{candidate("7", "Example Encode-GRP", 40)}
	sha, err := SetSHA(base)
	if err != nil || len(sha) != 64 {
		t.Fatalf("sha %q err %v", sha, err)
	}
	renamed := []pttdupe.Candidate{candidate("7", "Example Encode-OTHER", 40)}
	resized := []pttdupe.Candidate{candidate("7", "Example Encode-GRP", 41)}
	added := []pttdupe.Candidate{candidate("7", "Example Encode-GRP", 40), candidate("8", "Second Encode-GRP", 40)}
	for _, changed := range [][]pttdupe.Candidate{renamed, resized, added} {
		got, err := SetSHA(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got == sha {
			t.Fatalf("candidate change must change the sha: %+v", changed)
		}
	}
	reversed := []pttdupe.Candidate{base[0], candidate("8", "Second Encode-GRP", 40)}
	forward := []pttdupe.Candidate{candidate("8", "Second Encode-GRP", 40), base[0]}
	a, err := SetSHA(reversed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SetSHA(forward)
	if err != nil || a != b {
		t.Fatalf("sort order must not change the sha: %s %s", a, b)
	}
	withoutReasons := base[0]
	withoutReasons.Reasons = nil
	nilSHA, err := SetSHA([]pttdupe.Candidate{withoutReasons})
	if err != nil {
		t.Fatal(err)
	}
	emptyReasons := base[0]
	emptyReasons.Reasons = []string{}
	emptySHA, err := SetSHA([]pttdupe.Candidate{emptyReasons})
	if err != nil || nilSHA != emptySHA {
		t.Fatalf("nil and empty reasons must hash alike: %s %s", nilSHA, emptySHA)
	}
}

func TestHardRefuse(t *testing.T) {
	if err := AcceptableStatus(pttdupe.StatusFailed); err == nil || !strings.Contains(err.Error(), "FAILED") {
		t.Fatalf("FAILED: %v", err)
	}
	if err := AcceptableStatus(pttdupe.StatusNotRun); err == nil || !strings.Contains(err.Error(), "NOT_RUN") {
		t.Fatalf("NOT_RUN: %v", err)
	}
	if err := AcceptableStatus(pttdupe.StatusDuplicate); err != nil {
		t.Fatal(err)
	}
	zulea := candidate("1", "Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea", 10)
	if err := HardRefuse([]pttdupe.Candidate{zulea}); err == nil || !strings.Contains(err.Error(), "-Zulea") {
		t.Fatalf("group: %v", err)
	}
	exact := candidate("2", "Example Encode-GRP", 10)
	exact.Relation = "exact_duplicate"
	if err := HardRefuse([]pttdupe.Candidate{exact}); err == nil || !strings.Contains(err.Error(), "exact_duplicate") {
		t.Fatalf("exact: %v", err)
	}
	if err := HardRefuse([]pttdupe.Candidate{candidate("3", "Example Encode-GRP", 10)}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Other.Film.2014.REMUX-zulea", "Other.Film.2014.REMUX-ZULEA"} {
		lower := candidate("4", name, 10)
		if err := HardRefuse([]pttdupe.Candidate{lower}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "-zulea") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSearchRefusedIncompleteOrEmpty(t *testing.T) {
	one := []pttdupe.Candidate{candidate("7", "Encode-GRP", 40)}
	if err := SearchRefused(false, one); err == nil || !strings.Contains(err.Error(), "niekompletne") {
		t.Fatalf("incomplete: %v", err)
	}
	if err := SearchRefused(true, nil); err == nil || !strings.Contains(err.Error(), "pusta") {
		t.Fatalf("empty: %v", err)
	}
	if err := SearchRefused(true, []pttdupe.Candidate{}); err == nil || !strings.Contains(err.Error(), "pusta") {
		t.Fatalf("empty slice: %v", err)
	}
	if err := SearchRefused(true, one); err != nil {
		t.Fatal(err)
	}
}

func TestPhraseBindsReleaseAndSet(t *testing.T) {
	sha, err := SetSHA([]pttdupe.Candidate{candidate("7", "Encode-GRP", 40)})
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := AcceptPhrase("Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea", sha)
	if err != nil {
		t.Fatal(err)
	}
	short := ReleaseShort("Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea")
	if phrase != "AKCEPTUJE "+short+" "+sha[:12] || len(short) != 12 {
		t.Fatalf("phrase %q", phrase)
	}
	revoke, err := RevokePhrase("Example.Film.2019.MULTi.1080p.REMUX.BluRay.AVC.DTS-HD.MA.5.1-Zulea", sha)
	if err != nil || !strings.HasPrefix(revoke, "WYCOFUJE ") {
		t.Fatalf("revoke %q err %v", revoke, err)
	}
	if _, err := AcceptPhrase("rel", "abcd"); err == nil {
		t.Fatal("short sha must be refused")
	}
}

func decision(action, sha, reason string, cands []pttdupe.Candidate) Decision {
	return Decision{
		Action:       action,
		At:           "2026-11-05T00:00:00Z",
		Reason:       reason,
		CandidateSHA: sha,
		Candidates:   cands,
		Status:       pttdupe.StatusDuplicate,
		Version:      "0.6.0",
	}
}

func TestSaveIsAtomicPrivateAndKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dupe-accept", "Example-Zulea.ptt.json")
	cands := []pttdupe.Candidate{candidate("7", "Encode-GRP", 40)}
	sha, err := SetSHA(cands)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{Release: "Example-Zulea"}
	rec.Append(decision(ActionAccept, sha, "pierwsza", cands))
	if err := Save(path, rec); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for every writable file: os.Chmod only toggles
	// the read-only attribute, so 0600/0700 cannot be observed there. The
	// uploader runs on Linux, where these modes are enforced.
	if runtime.GOOS != "windows" {
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
		dst, derr := os.Stat(filepath.Dir(path))
		if derr != nil || dst.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v err %v", dst, derr)
		}
	}
	rec.Append(decision(ActionRevoke, sha, "wycofanie", cands))
	if err := Save(path, rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Active() != nil || len(loaded.History) != 2 || loaded.History[0].Reason != "pierwsza" || loaded.Reason != "wycofanie" {
		t.Fatalf("history was overwritten: %+v", loaded)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}
	rec.History[1].At = "2026-11-05T00:00:01Z"
	rec.At = rec.History[1].At
	if runtime.GOOS != "windows" {
		// A mode 0500 directory is a Unix check. Windows does not honor it.
		if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
		if err := Save(path, rec); err == nil {
			t.Fatal("save into a read-only directory must fail")
		}
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	again, err := Load(path)
	if err != nil || again.Reason != "wycofanie" || len(again.History) != 2 {
		t.Fatalf("failed save changed the record: %+v %v", again, err)
	}
}
