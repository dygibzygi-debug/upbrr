// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/autobrr/upbrr/internal/zulea/zuleatest"
)

func TestCheckAllWithoutNetwork(t *testing.T) {
	env, cfg, guard := setup(t)
	runner := &Runner{Config: cfg}
	summary, err := runner.Check(nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OK != 1 || summary.Stop != 0 || len(summary.Results) != 1 || summary.Results[0].Images != 3 {
		t.Fatalf("summary %+v", summary)
	}
	if len(guard.hosts) != 0 {
		t.Fatalf("check must not use the network, got %q", guard.hosts)
	}
	var out bytes.Buffer
	RenderCheck(&out, summary)
	if !strings.Contains(out.String(), "OK=1 STOP=0") || !strings.Contains(out.String(), "Przykładowy film / Example Film") {
		t.Fatalf("render:\n%s", out.String())
	}
	st, err := os.Stat(summary.ReportPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("report %s: %v", summary.ReportPath, err)
	}
	// A changed MKV turns the release into a STOP with the reason.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(env.MKVPath, later, later); err != nil {
		t.Fatal(err)
	}
	summary, err = runner.Check([]string{zuleatest.Release, "Missing.2000.PL.1080p.REMUX.BluRay.AVC.DD.2.0-Zulea"})
	if err != nil || summary.Stop != 2 || !strings.Contains(summary.Results[0].Reason, "ino/size/mtime") {
		t.Fatalf("stop summary %+v %v", summary, err)
	}
}

func TestVerifyIdentity(t *testing.T) {
	env, cfg, _ := setup(t)
	runner := &Runner{Config: cfg}
	rep, err := runner.VerifyIdentity(zuleatest.Release)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.IdentityOK || !rep.PathModeOK || rep.XattrModeOK {
		t.Fatalf("report %+v", rep)
	}
	var out bytes.Buffer
	RenderIdentity(&out, rep)
	if !strings.Contains(out.String(), "TOŻSAMOŚĆ_ZGODNA=TAK") || !strings.Contains(out.String(), "TRYB_PATH=TAK") {
		t.Fatalf("render:\n%s", out.String())
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(env.MKVPath, later, later); err != nil {
		t.Fatal(err)
	}
	if rep, err = runner.VerifyIdentity(zuleatest.Release); err != nil || rep.IdentityOK {
		t.Fatalf("changed MKV must report identity mismatch: %+v %v", rep, err)
	}
	if _, err := runner.VerifyIdentity("../x"); err == nil {
		t.Fatal("unsafe name must be rejected")
	}
}

func TestReceiptsListing(t *testing.T) {
	_, cfg, _ := setup(t)
	runner := &Runner{Config: cfg}
	if list, err := runner.Receipts(); err != nil || len(list) != 0 {
		t.Fatalf("empty: %v %v", list, err)
	}
	match := true
	if err := writeReceipt(runner.receiptPath("A-Zulea"), Receipt{
		Release:   "A-Zulea",
		State:     ReceiptAccepted,
		TorrentID: "7",
		DataMatch: &match,
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.WorkDir, "receipts", "B-Zulea.ptt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := runner.Receipts()
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	var out bytes.Buffer
	RenderReceipts(&out, list)
	if !strings.Contains(out.String(), "accepted") || !strings.Contains(out.String(), "unreadable") {
		t.Fatalf("render:\n%s", out.String())
	}
}
