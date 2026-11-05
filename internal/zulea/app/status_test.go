// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

func TestSeedStatusAndHistory(t *testing.T) {
	_, _, runner := uploadSetup(t)
	fp := dryRunFingerprint(t, runner)
	if _, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}); err != nil {
		t.Fatal(err)
	}
	qb := &recordingQB{}
	cases := []struct {
		present *seed.Torrent
		want    string
	}{
		{nil, SeedAbsent},
		{&seed.Torrent{State: "checkingUP", Progress: 0.5}, SeedChecking},
		{&seed.Torrent{State: "pausedDL", Progress: 0.7}, SeedPartial},
		{&seed.Torrent{State: "stoppedUP", Progress: 1}, SeedStopped},
		{&seed.Torrent{State: "stalledUP", Progress: 1}, SeedSeeding},
		{&seed.Torrent{State: "missingFiles", Progress: 1}, SeedError},
	}
	for _, c := range cases {
		qb.present = c.present
		results, err := runner.SeedStatus(context.Background(), qb)
		if err != nil || len(results) != 1 || results[0].Seed != c.want {
			t.Fatalf("state %+v: got %+v %v", c.present, results, err)
		}
	}
	if qb.added != "" || qb.started {
		t.Fatal("seed-status must be read only")
	}
	path := filepath.Join(t.TempDir(), "history.csv")
	n, err := runner.WriteHistory(path)
	if err != nil || n != 1 {
		t.Fatalf("history %d %v", n, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][0] != zuleatest.Release || rows[1][2] != ReceiptAccepted || rows[1][3] != "77" || rows[1][5] != "TAK" {
		t.Fatalf("rows %q %v", rows, err)
	}
	data, _ := os.ReadFile(path)
	for _, secret := range []string{pttKey, zuleatest.PasskeySentinel, "torrent/download"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("history leaks %s", secret)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Fatalf("history mode %v", st.Mode().Perm())
	}
}
