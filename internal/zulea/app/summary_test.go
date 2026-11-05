// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

func TestBuildSummary(t *testing.T) {
	_, _, runner := uploadSetup(t)
	if _, err := runner.Check(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ScanPTT(context.Background(), nil, 0); err != nil {
		t.Fatal(err)
	}
	s, err := runner.BuildSummary(context.Background(), nil)
	if err != nil || s.Attention || !strings.Contains(s.Text, "Łańcuch: OK=1 STOP=0") || !strings.Contains(s.Text, "PTT: CLEAR=1") ||
		!strings.Contains(s.Text, "Kolejka: 1") {
		t.Fatalf("summary %+v %v", s, err)
	}
	fp := dryRunFingerprint(t, runner)
	if _, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}); err != nil {
		t.Fatal(err)
	}
	qb := &recordingQB{present: &seed.Torrent{State: "pausedDL", Progress: 0.5}}
	s, err = runner.BuildSummary(context.Background(), qb)
	if err != nil || !s.Attention || !strings.Contains(s.Text, "Czeka na zatwierdzenie PTT: 1") || !strings.Contains(s.Text, "Seed: 0/1") {
		t.Fatalf("summary after upload %+v %v", s, err)
	}
	for _, secret := range []string{pttKey, tmdbKey, zuleatest.PasskeySentinel} {
		if strings.Contains(s.Text, secret) {
			t.Fatalf("summary leaks %s", secret)
		}
	}
	if _, err := runner.Notify(context.Background(), "x"); err == nil {
		t.Fatal("notify without configuration must fail")
	}
}
