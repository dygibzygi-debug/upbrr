// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autobrr/upbrr/internal/zulea/pttdupe"
	"github.com/autobrr/upbrr/internal/zulea/pttplan"
	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

const otherRelease = "Other.Film.2019.PL.1080p.REMUX.BluRay.AVC.DD.2.0-Zulea"

func gateOpen(runner *Runner) bool {
	return runner.canaryGate(&pttplan.Plan{TaxonomyConfirmed: true}, &pttdupe.Result{Status: "CLEAR"}, "k").Ready
}

func TestUnsettledReceiptsBlockQueueAndGate(t *testing.T) {
	cases := map[string]struct {
		receipt *Receipt
		raw     string
		blocks  bool
	}{
		"attempted":         {receipt: &Receipt{State: ReceiptAttempted}, blocks: true},
		"uncertain":         {receipt: &Receipt{State: ReceiptUncertain}, blocks: true},
		"rejected":          {receipt: &Receipt{State: ReceiptRejected}, blocks: true},
		"accepted pending":  {receipt: &Receipt{State: ReceiptAccepted, Moderation: "PENDING"}, blocks: true},
		"unknown state":     {receipt: &Receipt{State: "weird"}, blocks: true},
		"unreadable":        {raw: "{torn", blocks: true},
		"not_sent":          {receipt: &Receipt{State: ReceiptNotSent}},
		"accepted approved": {receipt: &Receipt{State: ReceiptAccepted, Moderation: "APPROVED"}},
		"rejected acknowledged": {receipt: &Receipt{
			State: ReceiptRejected,
			Resolutions: []Resolution{{
				From: ReceiptUncertain,
				To:   ReceiptRejected,
				Note: "n",
			}},
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, fake, runner := uploadSetup(t)
			if _, err := runner.ScanPTT(context.Background(), nil, 0); err != nil {
				t.Fatal(err)
			}
			path := runner.receiptPath(otherRelease)
			if tc.receipt != nil {
				tc.receipt.Release = otherRelease
				if err := writeReceipt(path, *tc.receipt, false); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := runner.Queue()
			if blocked := errors.Is(err, ErrQueueBlocked); blocked != tc.blocks {
				t.Fatalf("queue blocked=%v want %v (err %v)", blocked, tc.blocks, err)
			}
			if !tc.blocks && err != nil {
				t.Fatalf("queue: %v", err)
			}
			if gateOpen(runner) == tc.blocks {
				t.Fatalf("canary gate open=%v, want blocked=%v", !tc.blocks, tc.blocks)
			}
			if fake.uploads != 0 {
				t.Fatal("no upload expected")
			}
		})
	}
}

func resolveReq(state, id string, confirm func(string) (string, error)) ResolveRequest {
	return ResolveRequest{
		Release: zuleatest.Release,
		State:   state,
		PTTID:   id,
		Note:    "sprawdzone na stronie PTT",
		Preview: io.Discard,
		Confirm: confirm,
	}
}

// phraseFor answers with the correct phrase for the receipt currently on disk.
func phraseFor(t *testing.T, runner *Runner, state string) func(string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(runner.receiptPath(zuleatest.Release))
	if err != nil {
		t.Fatal(err)
	}
	return typed(ResolvePhrase(state, data))
}

func TestResolveUncertainAsAcceptedThenRejected(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	path := runner.receiptPath(zuleatest.Release)
	if err := writeReceipt(path, Receipt{
		Release:     zuleatest.Release,
		State:       ReceiptUncertain,
		Fingerprint: strings.Repeat("a", 64),
	}, true); err != nil {
		t.Fatal(err)
	}
	refusals := map[string]ResolveRequest{
		"wrong phrase": resolveReq(ReceiptNotSent, "", typed("ROZSTRZYGAM not_sent 000000000000")),
		"no note": {
			Release: zuleatest.Release,
			State:   ReceiptNotSent,
			Preview: io.Discard,
			Confirm: phraseFor(t, runner, ReceiptNotSent),
		},
		"accepted no id":   resolveReq(ReceiptAccepted, "", phraseFor(t, runner, ReceiptAccepted)),
		"id with not_sent": resolveReq(ReceiptNotSent, "77", phraseFor(t, runner, ReceiptNotSent)),
		"bad state":        resolveReq(ReceiptAttempted, "", phraseFor(t, runner, ReceiptAttempted)),
		"no confirm": {
			Release: zuleatest.Release,
			State:   ReceiptNotSent,
			Note:    "x",
			Preview: io.Discard,
		},
		"phrase of rejected": resolveReq(ReceiptNotSent, "", phraseFor(t, runner, ReceiptRejected)),
	}
	before, _ := os.ReadFile(path)
	for name, req := range refusals {
		if _, err := runner.Resolve(req); !errors.Is(err, ErrUploadRefused) {
			t.Fatalf("%s: expected refusal, got %v", name, err)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused resolve must not change the receipt")
	}
	// A receipt changed while the human was typing is refused.
	changing := func(string) (string, error) {
		data, _ := os.ReadFile(path)
		phrase := ResolvePhrase(ReceiptNotSent, data)
		_ = os.WriteFile(path, append(data, ' '), 0o600)
		return phrase, nil
	}
	if _, err := runner.Resolve(resolveReq(ReceiptNotSent, "", changing)); !errors.Is(err, ErrUploadRefused) {
		t.Fatalf("changed receipt must be refused: %v", err)
	}
	got, err := runner.Resolve(resolveReq(ReceiptAccepted, "77", phraseFor(t, runner, ReceiptAccepted)))
	if err != nil || got.State != ReceiptAccepted || got.TorrentID != "77" || len(got.Resolutions) != 1 || got.Resolutions[0].From != ReceiptUncertain {
		t.Fatalf("resolve accepted: %+v %v", got, err)
	}
	if blockReason(got) == "" {
		t.Fatal("an accepted upload without APPROVED must still block")
	}
	got, err = runner.Resolve(resolveReq(ReceiptRejected, "", phraseFor(t, runner, ReceiptRejected)))
	if err != nil || got.State != ReceiptRejected || blockReason(got) != "" || len(got.Resolutions) != 2 {
		t.Fatalf("resolve rejected: %+v %v", got, err)
	}
	if _, err := runner.Resolve(resolveReq(ReceiptRejected, "", phraseFor(t, runner, ReceiptRejected))); !errors.Is(err, ErrUploadRefused) {
		t.Fatalf("an acknowledged rejection cannot be resolved again: %v", err)
	}
	// A rejected release is still never uploaded again automatically.
	if err := runner.checkNoReceipt(zuleatest.Release); !errors.Is(err, ErrUploadRefused) || fake.uploads != 0 {
		t.Fatalf("rejected release must stay refused: %v", err)
	}
}

func TestResolveNotSentAllowsReuploadAndKeepsEvidence(t *testing.T) {
	_, fake, runner := uploadSetup(t)
	path := runner.receiptPath(zuleatest.Release)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runner.Resolve(resolveReq(ReceiptNotSent, "", phraseFor(t, runner, ReceiptNotSent)))
	if err != nil || got.State != ReceiptNotSent || got.Resolutions[0].From != ReceiptUnreadable {
		t.Fatalf("resolve unreadable: %+v %v", got, err)
	}
	fp := dryRunFingerprint(t, runner)
	if _, err := runner.Upload(context.Background(), UploadRequest{
		Release:     zuleatest.Release,
		Fingerprint: fp,
		Preview:     io.Discard,
		Confirm:     typed(ConfirmationPhrase(fp)),
	}); err != nil || fake.uploads != 1 {
		t.Fatalf("a resolved not_sent release may be uploaded once more: %v uploads=%d", err, fake.uploads)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary receipt left behind: %s", e.Name())
		}
		if st, err := e.Info(); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s must be 0600", e.Name())
		}
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, ".unreadable-") || !strings.Contains(joined, ".superseded-") {
		t.Fatalf("evidence files missing: %s", joined)
	}
	receipts, err := runner.Receipts()
	if err != nil || len(receipts) != 1 || receipts[0].State != ReceiptAccepted {
		t.Fatalf("only the live receipt is listed: %+v %v", receipts, err)
	}
}
