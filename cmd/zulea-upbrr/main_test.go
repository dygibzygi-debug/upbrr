// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
